package haproxy

import (
	"strings"
	"testing"
)

// Under `balance first` the order of the server lines IS the routing policy:
// HAProxy sends every request to the first listed server that is up. This
// models that rule over the rendered config, so the test states what each
// order serves rather than only which line comes first.

// serverOrder is the server names of one backend, in rendered order.
func serverOrder(cfg, backend string) []string {
	var names []string
	in := false
	for _, line := range strings.Split(cfg, "\n") {
		if strings.HasPrefix(line, "backend ") {
			in = strings.TrimSpace(strings.TrimPrefix(line, "backend ")) == backend
			continue
		}
		f := strings.Fields(line)
		if in && len(f) >= 2 && f[0] == "server" {
			names = append(names, f[1])
		}
	}
	return names
}

// servedByFirst is `balance first`: the first listed server that is up.
func servedByFirst(order []string, up map[string]bool) string {
	for _, n := range order {
		if up[n] {
			return n
		}
	}
	return ""
}

func TestDeployBackendServesTheActiveSlot(t *testing.T) {
	cfg := genWithBackends(t, []Backend{{
		Name: "kc", DomainMatches: []string{"kc.example.com"},
		Deploy: true, CurrentServer: "192.0.2.10:6302", NextServer: "192.0.2.10:6300",
		CheckPath: "/health",
	}})
	if !strings.Contains(cfg, "balance first") {
		t.Fatalf("deploy backend is not balance first:\n%s", cfg)
	}
	order := serverOrder(cfg, BackendName("kc"))
	if len(order) != 2 {
		t.Fatalf("server lines = %v, want current and next:\n%s", order, cfg)
	}

	// The order this replaced, for contrast: with both slots up it served
	// the standby. After a promote + swap + reload (runtime states reset),
	// "next" is the slot still running the old code.
	if got := servedByFirst([]string{"next", "current"}, map[string]bool{"current": true, "next": true}); got != "next" {
		t.Fatalf("model check: next-first with both up served %q", got)
	}

	cases := []struct {
		name string
		up   map[string]bool
		want string
	}{
		{"both up: the active slot serves", map[string]bool{"current": true, "next": true}, "current"},
		{"current down: the standby takes over", map[string]bool{"next": true}, "next"},
		{"next down: current serves", map[string]bool{"current": true}, "current"},
		{"both down: nothing", map[string]bool{}, ""},
	}
	for _, c := range cases {
		if got := servedByFirst(order, c.up); got != c.want {
			t.Errorf("%s: served %q, want %q (order %v)", c.name, got, c.want, order)
		}
	}
}
