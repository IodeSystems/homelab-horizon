package main

import (
	"flag"
	"fmt"
	"strconv"
	"strings"
	"time"
)

func sleep1s() { time.Sleep(time.Second) }

// runPorts finds the next free contiguous port range on a host by scanning
// every service's proxy backend and deploy next-backend. Best-effort and
// client-side: horizon has no port reservation, so this only reports a free
// base you can then use — nothing is reserved.
func runPorts(c *client, args []string) error {
	if len(args) == 0 || args[0] != "next" {
		return fmt.Errorf("usage: hz ports next --host IP [--count N] [--from PORT]")
	}
	fs := flag.NewFlagSet("ports next", flag.ContinueOnError)
	host := fs.String("host", "", "host IP to scan backends for (required)")
	count := fs.Int("count", 1, "size of the contiguous range")
	from := fs.Int("from", 8000, "lowest port to consider")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *host == "" {
		return fmt.Errorf("--host is required")
	}
	if *count < 1 {
		return fmt.Errorf("--count must be >= 1")
	}

	list, err := fetchServices(c)
	if err != nil {
		return err
	}
	used := map[int]bool{}
	for _, s := range list {
		if s.Proxy == nil {
			continue
		}
		collectPort(used, *host, s.Proxy.Backend)
		if s.Proxy.Deploy != nil {
			collectPort(used, *host, s.Proxy.Deploy.NextBackend)
		}
	}

	base := findFreeRange(used, *from, *count)
	if base == 0 {
		return fmt.Errorf("no free %d-port range >= %d below 65536 on %s", *count, *from, *host)
	}
	if *count == 1 {
		fmt.Println(base)
	} else {
		fmt.Printf("%d-%d\n", base, base+*count-1)
	}
	return nil
}

func collectPort(used map[int]bool, host, backend string) {
	h, p, ok := splitHostPort(backend)
	if !ok || h != host {
		return
	}
	used[p] = true
}

func splitHostPort(s string) (string, int, bool) {
	i := strings.LastIndex(s, ":")
	if i < 0 {
		return "", 0, false
	}
	p, err := strconv.Atoi(s[i+1:])
	if err != nil {
		return "", 0, false
	}
	return s[:i], p, true
}

func findFreeRange(used map[int]bool, from, count int) int {
	for base := from; base+count-1 <= 65535; base++ {
		free := true
		for p := base; p < base+count; p++ {
			if used[p] {
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
