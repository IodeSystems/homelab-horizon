package server

import (
	"bufio"
	"encoding/json"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"homelab-horizon/internal/apitypes"
)

// handleAPISystemMetrics returns a single point-in-time snapshot of host
// metrics. CPU + network counters are raw cumulative values from /proc;
// the UI subtracts adjacent samples to derive rates. Stateless on purpose
// — multiple clients can poll at different cadences without interfering.
func (s *Server) handleAPISystemMetrics(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	resp := apitypes.SystemMetricsResponse{
		TS:    time.Now().UnixMilli(),
		CPU:   readCPU(),
		Memory: readMemory(),
		Network: readNetwork(),
		Disks: readDisks(),
	}
	resp.Load1, resp.Load5, resp.Load15 = readLoadAvg()
	resp.UptimeSeconds = readUptime()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func readCPU() apitypes.CPUMetric {
	m := apitypes.CPUMetric{Cores: runtime.NumCPU()}
	f, err := os.Open("/proc/stat")
	if err != nil {
		return m
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}
		// "cpu  user nice system idle iowait irq softirq steal guest guest_nice"
		fields := strings.Fields(line)
		if len(fields) < 9 {
			break
		}
		m.User = parseUint(fields[1])
		m.Nice = parseUint(fields[2])
		m.System = parseUint(fields[3])
		m.Idle = parseUint(fields[4])
		m.IOWait = parseUint(fields[5])
		m.IRQ = parseUint(fields[6])
		m.SoftIRQ = parseUint(fields[7])
		m.Steal = parseUint(fields[8])
		break
	}
	return m
}

func readMemory() apitypes.MemoryMetric {
	m := apitypes.MemoryMetric{}
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return m
	}
	defer f.Close()
	var total, avail, swapTotal, swapFree uint64
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		colon := strings.IndexByte(line, ':')
		if colon < 0 {
			continue
		}
		key := line[:colon]
		// Values in /proc/meminfo are kB.
		val := parseUint(strings.Fields(strings.TrimSpace(line[colon+1:]))[0]) * 1024
		switch key {
		case "MemTotal":
			total = val
		case "MemAvailable":
			avail = val
		case "SwapTotal":
			swapTotal = val
		case "SwapFree":
			swapFree = val
		}
	}
	m.TotalBytes = total
	m.AvailableBytes = avail
	if total > avail {
		m.UsedBytes = total - avail
	}
	m.SwapTotalBytes = swapTotal
	if swapTotal > swapFree {
		m.SwapUsedBytes = swapTotal - swapFree
	}
	return m
}

func readNetwork() []apitypes.NetworkIface {
	out := []apitypes.NetworkIface{}
	f, err := os.Open("/proc/net/dev")
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	// Skip 2 header lines.
	sc.Scan()
	sc.Scan()
	for sc.Scan() {
		line := sc.Text()
		colon := strings.IndexByte(line, ':')
		if colon < 0 {
			continue
		}
		iface := strings.TrimSpace(line[:colon])
		if iface == "lo" {
			continue
		}
		fields := strings.Fields(strings.TrimSpace(line[colon+1:]))
		if len(fields) < 16 {
			continue
		}
		// Layout: rx bytes, packets, errs, drop, fifo, frame, compressed,
		// multicast, then tx bytes, packets, errs, ...
		rx := parseUint(fields[0])
		tx := parseUint(fields[8])
		out = append(out, apitypes.NetworkIface{Iface: iface, RXBytes: rx, TXBytes: tx})
	}
	return out
}

// realFSTypes lists filesystems we want to surface in the disk gauge. We
// skip pseudo / virtual FS (proc, sysfs, tmpfs, cgroup, overlay, etc.) —
// those don't represent actual storage capacity the admin can run out of.
var realFSTypes = map[string]bool{
	"ext2":    true,
	"ext3":    true,
	"ext4":    true,
	"xfs":     true,
	"btrfs":   true,
	"zfs":     true,
	"f2fs":    true,
	"vfat":    true,
	"exfat":   true,
	"ntfs":    true,
	"ntfs3":   true,
	"jfs":     true,
	"reiserfs": true,
}

func readDisks() []apitypes.DiskMetric {
	out := []apitypes.DiskMetric{}
	f, err := os.Open("/proc/mounts")
	if err != nil {
		return out
	}
	defer f.Close()
	seen := map[string]bool{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		// Format: device mount fstype opts dump pass
		fields := strings.Fields(sc.Text())
		if len(fields) < 3 {
			continue
		}
		mount := fields[1]
		fsType := fields[2]
		if !realFSTypes[fsType] {
			continue
		}
		if seen[mount] {
			continue
		}
		seen[mount] = true
		var st syscall.Statfs_t
		if err := syscall.Statfs(mount, &st); err != nil {
			continue
		}
		bs := uint64(st.Bsize)
		total := st.Blocks * bs
		free := st.Bavail * bs
		used := uint64(0)
		if total > free {
			used = total - free
		}
		out = append(out, apitypes.DiskMetric{
			Mount:      mount,
			FSType:     fsType,
			TotalBytes: total,
			FreeBytes:  free,
			UsedBytes:  used,
		})
	}
	return out
}

func readLoadAvg() (float64, float64, float64) {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, 0, 0
	}
	fields := strings.Fields(string(data))
	if len(fields) < 3 {
		return 0, 0, 0
	}
	l1, _ := strconv.ParseFloat(fields[0], 64)
	l5, _ := strconv.ParseFloat(fields[1], 64)
	l15, _ := strconv.ParseFloat(fields[2], 64)
	return l1, l5, l15
}

func readUptime() float64 {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(data))
	if len(fields) < 1 {
		return 0
	}
	v, _ := strconv.ParseFloat(fields[0], 64)
	return v
}

func parseUint(s string) uint64 {
	v, _ := strconv.ParseUint(s, 10, 64)
	return v
}
