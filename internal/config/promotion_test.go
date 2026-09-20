package config

import (
	"errors"
	"strings"
	"testing"
)

// The promotion edge: which rung may be promoted into which, and why a refusal is
// one of two different refusals with two different remedies.
//
// These are the rules plan/architecture.md step 6 rests on. Everything here runs
// on declared records alone — no keys, no values, no database — which is what lets
// hz answer the question at all when it can read nothing.

// promoLadder is redline's shape: dev → staging → prod, plus a lateral rung that
// borrows staging's posture without climbing.
func promoLadder() *Config {
	return &Config{
		Projects: []Project{{Name: "redline"}, {Name: "veliode"}},
		Environments: []Environment{
			{Project: "redline", Name: "dev", Posture: "dev"},
			{Project: "redline", Name: "staging", Posture: "staging", From: "dev"},
			{Project: "redline", Name: "prod", Posture: "prod", From: "staging"},
			// A disposable box borrowing staging's posture. It has an edge and it
			// does not climb: the forcible case, deliberately.
			{Project: "redline", Name: "loadtest", Posture: "staging", From: "staging"},
			// Declared with no From at all: there is no edge into it from
			// anywhere, and no flag can invent one.
			{Project: "redline", Name: "orphan", Posture: "prod"},
			// Every project gets to have a prod. The name alone is not an
			// identity.
			{Project: "veliode", Name: "prod", Posture: "prod", From: "beta"},
			{Project: "veliode", Name: "beta", Posture: "staging"},
		},
	}
}

func env(t *testing.T, c *Config, project, name string) Environment {
	t.Helper()
	e, err := c.LookupEnvironment(project, name)
	if err != nil {
		t.Fatalf("lookup %s/%s: %v", project, name, err)
	}
	return e
}

// TestPromotionUpwardIsAllowed: the whole point of the ladder. staging → prod
// climbs by PostureRank and the edge is declared, so nothing refuses it.
func TestPromotionUpwardIsAllowed(t *testing.T) {
	c := promoLadder()
	if err := CheckPromotion(env(t, c, "redline", "staging"), env(t, c, "redline", "prod")); err != nil {
		t.Fatalf("staging → prod must be allowed: %v", err)
	}
	if err := CheckPromotion(env(t, c, "redline", "dev"), env(t, c, "redline", "staging")); err != nil {
		t.Fatalf("dev → staging must be allowed: %v", err)
	}
}

// TestPromotionDownwardIsRefused: prod → staging is a demotion, and it is refused
// even though prod's config is the one that has actually run. The refusal is
// ErrNotUpward specifically, because that is the ONLY refusal --force may reach.
//
// It must not rest on string order: "prod" < "staging" alphabetically, so a
// string comparison would read this very promotion as an ascent.
func TestPromotionDownwardIsRefused(t *testing.T) {
	c := promoLadder()
	// The declared edge runs staging → prod, so the reverse fails on the edge
	// first: prod's own From is staging, and staging declares From dev.
	err := CheckPromotion(env(t, c, "redline", "prod"), env(t, c, "redline", "staging"))
	if err == nil {
		t.Fatal("prod → staging must be refused")
	}
	if !errors.Is(err, ErrWrongPromotionSource) {
		t.Fatalf("want a wrong-source refusal, got %v", err)
	}

	// And with the edge declared the other way round, the DIRECTION alone is
	// what refuses it — which is the case --force exists for.
	down := Environment{Project: "redline", Name: "staging", Posture: "staging", From: "prod"}
	err = CheckPromotion(env(t, c, "redline", "prod"), down)
	if !errors.Is(err, ErrNotUpward) {
		t.Fatalf("want ErrNotUpward for prod → staging, got %v", err)
	}
	if !strings.Contains(err.Error(), "prod") || !strings.Contains(err.Error(), "staging") {
		t.Errorf("the refusal must name both rungs: %v", err)
	}
}

// TestPromotionLateralIsRefused: a rung that borrows a posture without climbing is
// not a promotion. Refused, but ErrNotUpward — the forcible kind, because
// loadtest and virgin are real and intended shapes.
func TestPromotionLateralIsRefused(t *testing.T) {
	c := promoLadder()
	err := CheckPromotion(env(t, c, "redline", "staging"), env(t, c, "redline", "loadtest"))
	if !errors.Is(err, ErrNotUpward) {
		t.Fatalf("staging → loadtest is lateral and must refuse as not-upward, got %v", err)
	}
}

// TestPromotionWithNoFromIsAnErrorNamingTheEdge: an environment with no `from` is
// one nobody has said may be promoted into. The error has to name the missing
// edge, because "declare from on orphan" is the fix and a bare refusal is not.
func TestPromotionWithNoFromIsAnErrorNamingTheEdge(t *testing.T) {
	c := promoLadder()
	err := CheckPromotion(env(t, c, "redline", "staging"), env(t, c, "redline", "orphan"))
	if !errors.Is(err, ErrNoPromotionEdge) {
		t.Fatalf("want ErrNoPromotionEdge, got %v", err)
	}
	for _, want := range []string{"orphan", "redline", "from", "staging"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the missing-edge error must name %q: %v", want, err)
		}
	}
}

// TestPromotionFromTheWrongSourceIsRefused: prod promotes from staging, so dev →
// prod skips the rung that generates the evidence.
func TestPromotionFromTheWrongSourceIsRefused(t *testing.T) {
	c := promoLadder()
	err := CheckPromotion(env(t, c, "redline", "dev"), env(t, c, "redline", "prod"))
	if !errors.Is(err, ErrWrongPromotionSource) {
		t.Fatalf("want ErrWrongPromotionSource for dev → prod, got %v", err)
	}
}

// TestPromotionIsWithinOneProject: veliode's beta cannot promote into redline's
// prod, even though the postures would allow it. An environment is (project,
// name), and the edge is a statement inside one project.
func TestPromotionIsWithinOneProject(t *testing.T) {
	c := promoLadder()
	err := CheckPromotion(env(t, c, "veliode", "beta"), env(t, c, "redline", "prod"))
	if !errors.Is(err, ErrWrongPromotionSource) {
		t.Fatalf("want a cross-project refusal, got %v", err)
	}
}

// TestLookupEnvironmentNeedsAProjectWhenAmbiguous: two projects declare "prod", so
// the bare name resolves to nothing and says which projects to choose between.
func TestLookupEnvironmentNeedsAProjectWhenAmbiguous(t *testing.T) {
	c := promoLadder()
	_, err := c.LookupEnvironment("", "prod")
	if !errors.Is(err, ErrAmbiguousEnvironment) {
		t.Fatalf("want ErrAmbiguousEnvironment, got %v", err)
	}
	for _, want := range []string{"redline", "veliode"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the ambiguity must name %q: %v", want, err)
		}
	}
	if _, err := c.LookupEnvironment("redline", "prod"); err != nil {
		t.Fatalf("naming the project must resolve it: %v", err)
	}
	// A name only one project uses needs no project.
	if _, err := c.LookupEnvironment("", "loadtest"); err != nil {
		t.Fatalf("an unambiguous name must resolve bare: %v", err)
	}
}

func TestLookupEnvironmentRefusesAnUndeclaredName(t *testing.T) {
	c := promoLadder()
	if _, err := c.LookupEnvironment("", "ghost"); !errors.Is(err, ErrNoSuchEnvironment) {
		t.Fatalf("want ErrNoSuchEnvironment, got %v", err)
	}
	if _, err := c.LookupEnvironment("redline", "beta"); !errors.Is(err, ErrNoSuchEnvironment) {
		t.Fatalf("beta is veliode's, not redline's: got %v", err)
	}
}
