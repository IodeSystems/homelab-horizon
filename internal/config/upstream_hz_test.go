package config

import (
	"strings"
	"testing"
)

func TestValidateUpstream(t *testing.T) {
	good := func() *UpstreamHZ {
		return &UpstreamHZ{
			URL: "http://10.100.0.1:8080", TokenFile: "/etc/homelab-horizon/upstream.token",
			Rungs: []UpstreamRung{{Project: "redline", Environment: "prod"}},
		}
	}
	if err := (&Config{Upstream: good()}).ValidateUpstream(); err != nil {
		t.Fatalf("good upstream refused: %v", err)
	}
	if err := (&Config{}).ValidateUpstream(); err != nil {
		t.Fatalf("no upstream refused: %v", err)
	}
	for name, tc := range map[string]struct {
		mut  func(*UpstreamHZ)
		want string
	}{
		"ftp url":   {func(u *UpstreamHZ) { u.URL = "ftp://10.100.0.1" }, "http:// or https://"},
		"no host":   {func(u *UpstreamHZ) { u.URL = "http://" }, "no host"},
		"empty url": {func(u *UpstreamHZ) { u.URL = "" }, "needs a URL"},
		"no token":  {func(u *UpstreamHZ) { u.TokenFile = " " }, "token_file is required"},
		"no rungs":  {func(u *UpstreamHZ) { u.Rungs = nil }, "no rungs"},
		"half rung": {func(u *UpstreamHZ) { u.Rungs = []UpstreamRung{{Project: "redline"}} }, "both a project and an environment"},
		"twice":     {func(u *UpstreamHZ) { u.Rungs = append(u.Rungs, u.Rungs[0]) }, "listed twice"},
		"negative":  {func(u *UpstreamHZ) { u.PollSeconds = -1 }, "negative"},
	} {
		u := good()
		tc.mut(u)
		err := (&Config{Upstream: u}).ValidateUpstream()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", name, err, tc.want)
		}
	}
}

// The instance token's digest is minted, never edited: a URL edit keeps it.
// A token is for an hz instance only — never a machine of the same name.
func TestInstanceTokenHashSurvivesAnInstanceEdit(t *testing.T) {
	c := &Config{HZInstances: []HZInstance{{Name: "prod-hz", URL: "http://10.0.0.2:8080"}}, Machines: []Machine{{Name: "plain"}}}
	sha := strings.Repeat("c", 64)
	if err := c.SetInstanceTokenHash("prod-hz", sha); err != nil {
		t.Fatal(err)
	}
	if err := c.SetInstanceTokenHash("plain", sha); err == nil || !strings.Contains(err.Error(), "no hz instance") {
		t.Fatalf("token for a machine: %v", err)
	}
	if err := c.SetInstanceTokenHash("nope", sha); err == nil {
		t.Fatal("token for an undeclared instance")
	}
	u := "https://prod-hz.example"
	if _, err := c.SetHZInstance("prod-hz", HZInstancePatch{URL: &u}); err != nil {
		t.Fatal(err)
	}
	h, _ := c.FindHZInstance("prod-hz")
	if h.URL != u || h.TokenSHA256 != sha {
		t.Fatalf("after a URL edit: %+v", h)
	}
}
