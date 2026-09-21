package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A REFUSAL HAS TO ARRIVE AS WORDS, NOT AS A STATUS CODE.
//
// hz disarms the agent by refusing to serve desired state when the machine is
// in an HA fleet (internal/server/agent_fleet_guard.go,
// plan/ha-and-the-agent.md). That refusal is the whole operator-facing product
// of the guard on this side: it is what `journalctl -u hz-agent` and
// `hz-agent diff` show. A client that reduced it to "hz answered 409" would
// leave the operator with a number and no cause, on a box that has quietly
// stopped converging — which is the failure the guard exists to make loud.
//
// The 512-byte bound on the body read is why the refusal message is written to
// fit; this asserts the part that names the cause survives it.
func TestARefusedPollSaysWhy(t *testing.T) {
	const refusal = `{"error":"hz-agent is disarmed on this machine because HA peer-sync is configured ` +
		`(peer_id=\"site-b\", 1 peer(s) in config.json). See plan/ha-and-the-agent.md."}`

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(refusal))
	}))
	defer ts.Close()

	src := &HTTPSource{BaseURL: ts.URL, Token: "irrelevant"}
	d, etag, changed, err := src.Fetch(context.Background(), "previous-etag")
	if err == nil {
		t.Fatal("a refused poll returned no error")
	}
	for _, want := range []string{"409", "peer-sync", "ha-and-the-agent.md"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the agent's message does not mention %q: %v", want, err)
		}
	}
	// And it changes nothing: no payload, the caller keeps the ETag it had, and
	// there is nothing for it to plan against. A refused agent is a reporting
	// agent, not a writing one.
	if d != nil {
		t.Error("a refusal produced a payload")
	}
	if changed {
		t.Error("a refusal was reported as a change")
	}
	if etag != "previous-etag" {
		t.Errorf("the ETag moved on a refusal: %q", etag)
	}
}
