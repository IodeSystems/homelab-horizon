package probe

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testSet(hosts ...string) TargetSet {
	ts := TargetSet{Interval: 30}
	for _, h := range hosts {
		ts.Targets = append(ts.Targets, Target{Name: h, Host: h, Kinds: []string{KindDNS}})
	}
	ts.Version = ts.ComputeVersion()
	return ts
}

func TestComputeVersionIgnoresVersionField(t *testing.T) {
	a := testSet("a.example.com")
	b := a
	b.Version = "something-else"
	if a.ComputeVersion() != b.ComputeVersion() {
		t.Fatal("version must not depend on the version field, or it could never converge")
	}
}

func TestComputeVersionChangesWithTargets(t *testing.T) {
	a := testSet("a.example.com")
	b := testSet("a.example.com", "b.example.com")
	if a.Version == b.Version {
		t.Fatal("a changed target list must produce a new version")
	}
}

// The handshake is the protocol's whole point: hz names a version, the agent
// says whether it holds it, hz sends the set only when it does not.
func TestPollHandshake(t *testing.T) {
	agent := NewAgent("vps", "test", "tok", "")
	want := testSet("a.example.com")

	// First poll: the agent holds nothing, so it asks.
	resp := agent.Poll(PollRequest{TargetsVersion: want.Version})
	if !resp.WantTargets {
		t.Fatal("a fresh agent must ask for the target set")
	}
	if resp.TargetCount != 0 {
		t.Fatalf("fresh agent reported %d targets", resp.TargetCount)
	}

	// Second poll carries the set.
	set := want
	resp = agent.Poll(PollRequest{TargetsVersion: want.Version, Targets: &set})
	if resp.WantTargets {
		t.Fatal("the agent must stop asking once it holds the set")
	}
	if resp.TargetsVersion != want.Version {
		t.Fatalf("agent holds %q, expected %q", resp.TargetsVersion, want.Version)
	}
	if resp.TargetCount != 1 {
		t.Fatalf("agent reported %d targets, expected 1", resp.TargetCount)
	}

	// Steady state: no set on the wire.
	resp = agent.Poll(PollRequest{TargetsVersion: want.Version})
	if resp.WantTargets {
		t.Fatal("steady-state poll must not re-request the set")
	}

	// hz changes the targets: the agent asks again.
	next := testSet("a.example.com", "b.example.com")
	resp = agent.Poll(PollRequest{TargetsVersion: next.Version})
	if !resp.WantTargets {
		t.Fatal("a version the agent does not hold must trigger a request")
	}
}

// An agent hz has never configured must not ask when hz did not name a
// version either — that is a caller with no opinion, not a mismatch.
func TestPollNoVersionDoesNotAsk(t *testing.T) {
	agent := NewAgent("vps", "test", "tok", "")
	if agent.Poll(PollRequest{}).WantTargets {
		t.Fatal("a request with no target version must not be answered with a request")
	}
}

func TestSinceReturnsOnlyNewerResults(t *testing.T) {
	agent := NewAgent("vps", "test", "tok", "")
	base := time.Now().UTC().Truncate(time.Second)
	agent.Record([]Result{
		{Target: "a", At: base, Status: StatusOK},
		{Target: "b", At: base.Add(time.Second), Status: StatusOK},
		{Target: "c", At: base.Add(2 * time.Second), Status: StatusFailed},
	})

	got, truncated := agent.Since(base, 0)
	if len(got) != 2 || got[0].Target != "b" || got[1].Target != "c" {
		t.Fatalf("expected b and c after the watermark, got %+v", got)
	}
	if truncated {
		t.Fatal("two results under the cap must not report truncation")
	}

	got, truncated = agent.Since(base, 1)
	if len(got) != 1 || got[0].Target != "b" {
		t.Fatalf("limit must return the oldest unseen result first, got %+v", got)
	}
	if !truncated {
		t.Fatal("a capped answer must say more remain, or hz stops asking")
	}

	if got, _ = agent.Since(base.Add(time.Hour), 0); len(got) != 0 {
		t.Fatalf("watermark past every result must return nothing, got %+v", got)
	}
}

func TestRingBufferBoundsMemory(t *testing.T) {
	agent := NewAgent("vps", "test", "tok", "")
	batch := make([]Result, maxBuffered+100)
	for i := range batch {
		batch[i] = Result{Target: "a", At: time.Now().UTC(), Status: StatusOK}
	}
	agent.Record(batch)
	agent.mu.Lock()
	n := len(agent.results)
	agent.mu.Unlock()
	if n != maxBuffered {
		t.Fatalf("buffer held %d results, expected the cap of %d", n, maxBuffered)
	}
}

// The disk cache is what lets a restarted agent keep probing the right names
// while hz is unreachable — which is the window it was deployed to observe.
func TestStateSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "state.json")
	want := testSet("a.example.com")

	NewAgent("vps", "test", "tok", path).SetTargets(want)

	restarted := NewAgent("vps", "test", "tok", path)
	got := restarted.Targets()
	if got.Version != want.Version || len(got.Targets) != 1 {
		t.Fatalf("restarted agent lost its targets: %+v", got)
	}
	if restarted.Poll(PollRequest{TargetsVersion: want.Version}).WantTargets {
		t.Fatal("a restarted agent holding the set must not ask for it again")
	}
}

func TestCorruptStateIsIgnored(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	agent := NewAgent("vps", "test", "tok", path)
	if len(agent.Targets().Targets) != 0 {
		t.Fatal("a corrupt cache must leave the agent empty, not crash it")
	}
}

func TestHandlerRequiresToken(t *testing.T) {
	agent := NewAgent("vps", "test", "sekret", "")
	srv := httptest.NewServer(agent.Handler())
	defer srv.Close()

	cases := []struct {
		name, header string
		want         int
	}{
		{"no header", "", http.StatusUnauthorized},
		{"wrong token", "Bearer nope", http.StatusUnauthorized},
		{"raw token without scheme", "sekret", http.StatusUnauthorized},
		{"correct token", "Bearer sekret", http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/poll", strings.NewReader(`{}`))
			if err != nil {
				t.Fatal(err)
			}
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != tc.want {
				t.Fatalf("got %d, want %d", resp.StatusCode, tc.want)
			}
		})
	}
}

func TestHealthzIsOpenAndSaysNothing(t *testing.T) {
	agent := NewAgent("vps-nyc", "test", "sekret", "")
	srv := httptest.NewServer(agent.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz returned %d", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	// An unauthenticated caller learns liveness and nothing else — not the
	// vantage name, not the targets, not the version.
	if len(body) != 1 || body["ok"] != true {
		t.Fatalf("healthz leaked detail to an unauthenticated caller: %+v", body)
	}
}
