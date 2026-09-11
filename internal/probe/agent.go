package probe

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// maxBuffered caps the agent's result ring. At the default 60s round with a
// handful of targets this is most of a day of history, which is the window
// that matters: hz asks for everything since its last successful poll, and
// the only reason that gap is ever long is that hz was down.
const maxBuffered = 5000

// defaultInterval is the probe round cadence when the target set omits one.
const defaultInterval = 60 * time.Second

// Agent is the remote vantage: a probe loop, a result buffer, and one
// endpoint hz polls. It holds no address for hz and initiates nothing.
type Agent struct {
	vantage   string
	version   string
	token     string
	statePath string

	mu      sync.Mutex
	targets TargetSet
	results []Result

	// wake nudges the probe loop when a new target set lands, so a fresh
	// agent produces its first results immediately rather than one interval
	// after hz finished configuring it.
	wake chan struct{}

	// probed fires when a probe round finishes. The push loop waits on it
	// rather than guessing how long a round takes: a round is bounded by the
	// slowest target's timeout, so any fixed delay either races it or wastes
	// time, and racing it means fresh results sit unsent for a full interval.
	probed chan struct{}
}

// persisted is the agent's on-disk state: the target set, and nothing about
// hz. It exists so an agent that restarts while hz is unreachable keeps
// probing the right names instead of idling until hz comes back — which is
// exactly the window the agent was deployed to observe.
type persisted struct {
	Targets TargetSet `json:"targets"`
}

// NewAgent builds an agent. statePath may be empty, which disables the target
// cache; the agent then asks hz for targets again after every restart.
func NewAgent(vantage, version, token, statePath string) *Agent {
	a := &Agent{
		vantage:   vantage,
		version:   version,
		token:     token,
		statePath: statePath,
		wake:      make(chan struct{}, 1),
		probed:    make(chan struct{}, 1),
	}
	a.loadState()
	return a
}

// loadState restores the cached target set. A missing or corrupt file is not
// an error: the agent simply has no targets and says so on the next poll.
func (a *Agent) loadState() {
	if a.statePath == "" {
		return
	}
	b, err := os.ReadFile(a.statePath)
	if err != nil {
		return
	}
	var p persisted
	if err := json.Unmarshal(b, &p); err != nil {
		slog.Warn("probe: ignoring unreadable state file", "path", a.statePath, "error", err)
		return
	}
	a.targets = p.Targets
	slog.Info("probe: restored targets from cache",
		"path", a.statePath, "version", p.Targets.Version, "targets", len(p.Targets.Targets))
}

// saveState writes the target cache. Failure is logged, not fatal: the agent
// still works, it just forgets its targets across a restart.
func (a *Agent) saveState(ts TargetSet) {
	if a.statePath == "" {
		return
	}
	b, err := json.MarshalIndent(persisted{Targets: ts}, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(a.statePath), 0o700); err != nil {
		slog.Warn("probe: could not create state directory", "error", err)
		return
	}
	tmp := a.statePath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		slog.Warn("probe: could not write state", "error", err)
		return
	}
	if err := os.Rename(tmp, a.statePath); err != nil {
		slog.Warn("probe: could not replace state", "error", err)
	}
}

// SetTargets installs a target set and wakes the probe loop.
func (a *Agent) SetTargets(ts TargetSet) {
	if ts.Version == "" {
		ts.Version = ts.ComputeVersion()
	}
	a.mu.Lock()
	a.targets = ts
	a.mu.Unlock()
	a.saveState(ts)
	select {
	case a.wake <- struct{}{}:
	default:
	}
}

// Targets returns the installed set.
func (a *Agent) Targets() TargetSet {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.targets
}

// Record appends results to the ring buffer, as the probe loop does. Exported
// so a caller that produces results another way can feed the same buffer.
func (a *Agent) Record(rs []Result) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.results = append(a.results, rs...)
	if len(a.results) > maxBuffered {
		a.results = a.results[len(a.results)-maxBuffered:]
	}
}

// Since returns buffered results newer than t, oldest first, capped at limit.
// The bool reports whether results were left behind, so hz knows to poll
// again rather than assume it has drained the buffer.
func (a *Agent) Since(t time.Time, limit int) ([]Result, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if limit <= 0 || limit > maxBuffered {
		limit = maxBuffered
	}
	// Results are appended in time order, so the first one past t starts the
	// answer.
	i := 0
	for ; i < len(a.results); i++ {
		if a.results[i].At.After(t) {
			break
		}
	}
	rest := a.results[i:]
	truncated := len(rest) > limit
	if truncated {
		rest = rest[:limit]
	}
	out := make([]Result, len(rest))
	copy(out, rest)
	return out, truncated
}

// RunOnce probes every target once and buffers the results.
func (a *Agent) RunOnce(ctx context.Context) []Result {
	ts := a.Targets()
	if len(ts.Targets) == 0 {
		return nil
	}
	var (
		wg  sync.WaitGroup
		mu  sync.Mutex
		out []Result
	)
	for _, t := range ts.Targets {
		wg.Add(1)
		go func(t Target) {
			defer wg.Done()
			rs := Run(ctx, ts, t)
			mu.Lock()
			out = append(out, rs...)
			mu.Unlock()
		}(t)
	}
	wg.Wait()
	a.Record(out)

	// Tell anyone waiting that there is something to send.
	if len(out) > 0 {
		select {
		case a.probed <- struct{}{}:
		default:
		}
	}
	return out
}

// Probed is closed-over notification that a probe round produced results.
// The push loop selects on it so a report follows a round rather than a
// clock.
func (a *Agent) Probed() <-chan struct{} { return a.probed }

// Loop probes on the target set's interval until ctx is done. It keeps
// running whether or not hz ever polls; the buffer is what makes an hz
// outage observable after the fact instead of a hole in the history.
func (a *Agent) Loop(ctx context.Context) {
	for {
		a.RunOnce(ctx)

		interval := defaultInterval
		if n := a.Targets().Interval; n > 0 {
			interval = time.Duration(n) * time.Second
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-a.wake:
			timer.Stop()
		case <-timer.C:
		}
	}
}

// Poll answers hz.
//
// The handshake is one round trip in the steady state and two when the target
// set is new or changed: hz names the version it wants, the agent says
// WantTargets if that is not what it holds, and hz includes the set on its
// next poll. Results come back on every poll either way, so an agent waiting
// for targets still reports what it has.
func (a *Agent) Poll(req PollRequest) PollResponse {
	if req.Targets != nil {
		a.SetTargets(*req.Targets)
	}

	held := a.Targets()
	results, truncated := a.Since(req.Since, req.Limit)

	return PollResponse{
		Vantage:        a.vantage,
		Version:        a.version,
		Now:            time.Now().UTC(),
		TargetsVersion: held.Version,
		// Ask whenever the versions disagree, including the first poll of a
		// fresh agent, where the held version is empty.
		WantTargets: req.TargetsVersion != "" && req.TargetsVersion != held.Version,
		TargetCount: len(held.Targets),
		Results:     results,
		Truncated:   truncated,
	}
}

// Handler is the agent's HTTP surface: one polling endpoint and a liveness
// probe. Everything but liveness needs the token.
func (a *Agent) Handler() http.Handler {
	mux := http.NewServeMux()

	// Unauthenticated and deliberately empty of facts. It answers "this
	// process is up" for a load balancer or an uptime service, and nothing an
	// unauthenticated caller could learn from.
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}` + "\n"))
	})

	mux.HandleFunc("/v1/poll", a.requireToken(a.handlePoll))
	return mux
}

// requireToken gates a handler on the bearer token, compared in constant
// time. A wrong token gets 401 with no detail — an agent sitting on the
// public internet should not help anyone work out what it is.
func (a *Agent) requireToken(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// The scheme is required, not stripped if present: TrimPrefix on a
		// header with no scheme returns the header unchanged, which would
		// accept a bare token and quietly widen what counts as valid.
		header := r.Header.Get("Authorization")
		got, ok := strings.CutPrefix(header, "Bearer ")
		if !ok || subtle.ConstantTimeCompare([]byte(got), []byte(a.token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// maxRequestBody bounds a poll request. A target set is small; anything this
// large is a mistake or an attempt to exhaust the agent.
const maxRequestBody = 1 << 20

func (a *Agent) handlePoll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req PollRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBody))
	if err := dec.Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	resp := a.Poll(req)
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		slog.Warn("probe: could not write poll response", "error", err)
	}
}
