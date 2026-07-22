package main

import (
	"flag"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
)

func sleep1s() { time.Sleep(time.Second) }

const (
	// safeBandLow is the default lowest port allocation considers — well above
	// the crowded 0–9999 range where databases and dev tools cluster.
	safeBandLow = 20000
	// safeBandHigh is the top of the preferred band, kept below the typical
	// Linux ephemeral range (32768+) so fixed backends don't collide with
	// outbound connections' source ports.
	safeBandHigh = 32767
)

// commonPorts are well-known service/dev ports allocation always skips — even
// if --from drops into their range — so a horizon backend never lands on a port
// a database or dev tool expects to own.
var commonPorts = map[int]bool{
	// classic / system
	21: true, 22: true, 23: true, 25: true, 53: true, 80: true, 110: true,
	123: true, 143: true, 161: true, 389: true, 443: true, 465: true,
	587: true, 636: true, 993: true, 995: true, 3389: true, 5900: true,
	// databases
	1433: true, 1521: true, 3306: true, 5432: true, 5433: true, 6379: true,
	6380: true, 8086: true, 9042: true, 11211: true, 5984: true,
	27017: true, 27018: true, 27019: true,
	// search / analytics
	9200: true, 9300: true, 7700: true, 5601: true,
	// messaging / streaming
	1883: true, 2181: true, 4222: true, 6222: true, 8222: true, 5672: true,
	15672: true, 9092: true, 61616: true,
	// web / app dev servers
	3000: true, 3001: true, 4200: true, 5000: true, 5173: true, 8000: true,
	8008: true, 8080: true, 8081: true, 8443: true, 8888: true, 4040: true,
	// infra / orchestration / observability
	2375: true, 2376: true, 2379: true, 2380: true, 3100: true, 6443: true,
	8200: true, 8300: true, 8301: true, 8500: true, 8600: true, 9000: true,
	9090: true, 9091: true, 9093: true, 10250: true,
}

// commonRanges are contiguous bands dev tooling likes to spin things up in;
// allocation skips them entirely.
var commonRanges = [][2]int{
	{3000, 3010},
	{4000, 4010},
	{5000, 5010},
	{8000, 8099},
	{9000, 9099},
}

func isCommonPort(p int) bool {
	if commonPorts[p] {
		return true
	}
	for _, rg := range commonRanges {
		if p >= rg[0] && p <= rg[1] {
			return true
		}
	}
	return false
}

// runPorts reports port allocation on a host using the server's authoritative
// port map (service backends, blue-green deploy next-backends, HAProxy,
// WireGuard, dnsmasq, admin server). Best-effort and advisory: horizon has no
// port reservation, so this reports free ports you can then use — nothing is
// reserved.
func runPorts(c *client, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: hz ports <next|list> --host IP [--count N] [--from PORT]")
	}
	switch args[0] {
	case "next":
		return runPortsNext(c, args[1:])
	case "list":
		return runPortsList(c, args[1:])
	default:
		return fmt.Errorf("usage: hz ports <next|list> --host IP [--count N] [--from PORT]")
	}
}

func runPortsNext(c *client, args []string) error {
	fs := flag.NewFlagSet("ports next", flag.ContinueOnError)
	host := fs.String("host", "", "host IP to allocate on (required)")
	count := fs.Int("count", 1, "size of the contiguous range")
	from := fs.Int("from", safeBandLow, "lowest port to consider")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *host == "" {
		return fmt.Errorf("--host is required")
	}
	if *count < 1 {
		return fmt.Errorf("--count must be >= 1")
	}

	pm, err := fetchPortMap(c)
	if err != nil {
		return err
	}
	used := usedTCP(pm, *host)

	base := findFreeRange(used, *from, *count, excludedFunc(pm))
	if base == 0 {
		return fmt.Errorf("no free %d-port range >= %d below 65536 on %s (used + common ports excluded)", *count, *from, *host)
	}
	if *count == 1 {
		fmt.Println(base)
	} else {
		fmt.Printf("%d-%d\n", base, base+*count-1)
	}
	return nil
}

func runPortsList(c *client, args []string) error {
	fs := flag.NewFlagSet("ports list", flag.ContinueOnError)
	host := fs.String("host", "", "host IP to list ports for (required)")
	count := fs.Int("count", 5, "how many suggested free ports to show")
	from := fs.Int("from", safeBandLow, "lowest port to suggest from")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *host == "" {
		return fmt.Errorf("--host is required")
	}

	pm, err := fetchPortMap(c)
	if err != nil {
		return err
	}

	entries := append([]apitypes.HostPortEntry(nil), pm.Hosts[*host]...)
	sort.Slice(entries, func(i, j int) bool {
		return portNum(entries[i].Port) < portNum(entries[j].Port)
	})

	fmt.Printf("USED on %s (%d)\n", *host, len(entries))
	if len(entries) == 0 {
		fmt.Println("  (none)")
	} else {
		for _, e := range entries {
			label := e.Service
			if e.Domain != "" {
				label += "  " + e.Domain
			}
			fmt.Printf("  %-6s %-4s %s\n", e.Port, e.Proto, label)
		}
	}

	if len(pm.Exclusions.Custom) > 0 {
		fmt.Printf("\nCUSTOM EXCLUSIONS (%d) — edit in the Ports UI\n", len(pm.Exclusions.Custom))
		for _, r := range pm.Exclusions.Custom {
			span := strconv.Itoa(r.From)
			if r.To > r.From {
				span = fmt.Sprintf("%d-%d", r.From, r.To)
			}
			note := ""
			if r.Note != "" {
				note = "  " + r.Note
			}
			fmt.Printf("  %s%s\n", span, note)
		}
	}

	free := suggestFree(usedTCP(pm, *host), *from, *count, excludedFunc(pm))
	fmt.Printf("\nSUGGESTED FREE (safe band %d–%d, common + excluded ports skipped)\n", safeBandLow, safeBandHigh)
	if len(free) == 0 {
		fmt.Println("  (none available)")
		return nil
	}
	strs := make([]string, len(free))
	for i, p := range free {
		strs[i] = strconv.Itoa(p)
	}
	fmt.Printf("  %s\n", strings.Join(strs, "  "))
	return nil
}

func fetchPortMap(c *client) (apitypes.HostPortMapResponse, error) {
	var pm apitypes.HostPortMapResponse
	err := c.do("GET", "/api/v1/ports", nil, &pm)
	return pm, err
}

func portNum(s string) int {
	p, err := strconv.Atoi(s)
	if err != nil {
		return 1 << 30 // sort unparseable ports last
	}
	return p
}

// usedTCP is the set of TCP ports reserved on the host. UDP reservations
// (WireGuard, dnsmasq) don't block a TCP backend, so they're excluded here.
func usedTCP(pm apitypes.HostPortMapResponse, host string) map[int]bool {
	used := map[int]bool{}
	for _, e := range pm.Hosts[host] {
		if e.Proto != "" && e.Proto != "tcp" {
			continue
		}
		if p, err := strconv.Atoi(e.Port); err == nil {
			used[p] = true
		}
	}
	return used
}

// excludedFunc builds the port-denylist predicate from the server-provided
// exclusions (builtin + custom). Falls back to the CLI's hardcoded common-port
// list only when an older server returns no exclusions.
func excludedFunc(pm apitypes.HostPortMapResponse) func(int) bool {
	ranges := append(append([]apitypes.PortRange{}, pm.Exclusions.Builtin...), pm.Exclusions.Custom...)
	if len(ranges) == 0 {
		return isCommonPort
	}
	return func(p int) bool {
		for _, r := range ranges {
			hi := r.To
			if hi < r.From {
				hi = r.From
			}
			if p >= r.From && p <= hi {
				return true
			}
		}
		return false
	}
}

// findFreeRange returns the lowest base >= from where [base, base+count) is all
// free — skipping used ports and the excluded denylist.
func findFreeRange(used map[int]bool, from, count int, excluded func(int) bool) int {
	if from < 1 {
		from = 1
	}
	for base := from; base+count-1 <= 65535; base++ {
		free := true
		for p := base; p < base+count; p++ {
			if used[p] || excluded(p) {
				free = false
				base = p // skip past the conflict
				break
			}
		}
		if free {
			return base
		}
	}
	return 0
}

// suggestFree returns up to count free ports at or above from (never below the
// safe band), skipping used and excluded ports.
func suggestFree(used map[int]bool, from, count int, excluded func(int) bool) []int {
	if from < safeBandLow {
		from = safeBandLow
	}
	var out []int
	for p := from; p <= 65535 && len(out) < count; p++ {
		if used[p] || excluded(p) {
			continue
		}
		out = append(out, p)
	}
	return out
}
