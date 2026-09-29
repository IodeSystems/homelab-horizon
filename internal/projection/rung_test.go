package projection

import (
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/config"
)

func rungEstate() *config.Config {
	return &config.Config{
		Projects: []config.Project{{Name: "redline"}},
		Machines: []config.Machine{
			{Name: "redline-prod-hz", Project: "redline", HZ: &config.MachineHZ{URL: "https://hz.prod.redline.example"}},
			{Name: "build-1"},
		},
		Environments: []config.Environment{
			{Project: "redline", Name: "staging", Posture: "staging"},
			{Project: "redline", Name: "prod", Posture: "prod", From: "staging", Upstream: "redline-prod-hz"},
		},
	}
}

// A rung with an Upstream is a STATEMENT — placed in that hz — and never a
// Gap: this hz having no machine for it is the model being right
// (plan/design/estate.md Part A §5).
func TestAnUpstreamRungIsPlacedRemotelyNotAGap(t *testing.T) {
	cfg := rungEstate()
	prod, _ := cfg.LookupEnvironment("redline", "prod")
	p := PlaceRung(cfg, prod)
	if p.State != PlacementRemote || p.Upstream != "redline-prod-hz" || p.URL != "https://hz.prod.redline.example" {
		t.Fatalf("an upstream rung must be remote, naming the machine and its URL; got %+v", p)
	}
	if len(p.Unresolved) != 0 {
		t.Fatalf("an upstream rung must not be a gap; got %+v", p.Unresolved)
	}
	if !strings.Contains(p.Statement, "placed in redline-prod-hz") {
		t.Fatalf("the statement must say where it is placed; got %q", p.Statement)
	}

	staging, _ := cfg.LookupEnvironment("redline", "staging")
	h := PlaceRung(cfg, staging)
	if h.State != PlacementHere || h.Upstream != "" || len(h.Unresolved) != 0 || h.Statement == "" {
		t.Fatalf("a rung with no upstream is placed here, with a statement; got %+v", h)
	}
	if h.State == p.State {
		t.Fatal("here and remote collapsed into one state")
	}
}

// Records Save would refuse still reach a projection honestly: the projection
// says the upstream does not resolve rather than trusting the name.
func TestAnUnresolvableUpstreamIsSaidNotTrusted(t *testing.T) {
	cfg := rungEstate()
	for _, name := range []string{"ghost-hz", "build-1"} {
		p := PlaceRung(cfg, config.Environment{Project: "redline", Name: "prod", Posture: "prod", Upstream: name})
		if p.State != PlacementRemote || len(p.Unresolved) != 1 || p.Unresolved[0].Reason != ReasonUnmodelled || p.URL != "" {
			t.Fatalf("upstream %s: want remote with one unmodelled gap and no URL, got %+v", name, p)
		}
	}
}
