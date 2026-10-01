// Package nested is the CHILD side of a nested hz (plan/plan.md N4a): it pulls
// the applied desired state and artifact of each configured parent rung,
// verifies the artifact's sha256, caches both, and serves them locally — and
// keeps serving them when the parent is unreachable (CLAUDE.md #5).
//
// THE CHILD DIALS; THE PARENT NEVER DOES (CLAUDE.md #1). Every request in this
// package is outbound from the child: a GET of the parent's desired state, a
// GET of an artifact, and a POST of a forwarded deploy report. The parent
// needs no route to the child and holds no credential for it.
//
// decide.go is the pure half; this file and queue.go do the I/O.
package nested

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/iodesystems/homelab-horizon/hzapi"
	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/artifact"
	"github.com/iodesystems/homelab-horizon/internal/config"
)

// DefaultPoll is the desired-state poll interval when poll_seconds is 0.
const DefaultPoll = 30 * time.Second

// Child is one nested hz's puller, cache and report queue.
type Child struct {
	URL       string
	TokenFile string
	Rungs     []config.UpstreamRung
	Interval  time.Duration
	// Dir is <data dir>/upstream: the desired caches, the report queue, and
	// artifacts/ — a directory of its own, so pruning here can never reach an
	// artifact the parent side of this same hz was given.
	Dir   string
	Store artifact.Store
	HTTP  *http.Client
	Log   *slog.Logger
	// MaxArtifactBytes bounds a download; 0 is config.DefaultArtifactMaxBytes.
	MaxArtifactBytes int64

	mu    sync.Mutex
	links map[config.UpstreamRung]*Link
	queue *Queue
	kick  chan struct{}
}

// New builds a child from the config's upstream block and hz's data dir.
func New(u config.UpstreamHZ, dataDir string) *Child {
	dir := filepath.Join(dataDir, "upstream")
	interval := DefaultPoll
	if u.PollSeconds > 0 {
		interval = time.Duration(u.PollSeconds) * time.Second
	}
	c := &Child{
		URL:       strings.TrimRight(strings.TrimSpace(u.URL), "/"),
		TokenFile: u.TokenFile,
		Rungs:     append([]config.UpstreamRung(nil), u.Rungs...),
		Interval:  interval,
		Dir:       dir,
		Store:     artifact.Store{Dir: filepath.Join(dir, "artifacts")},
		Log:       slog.Default(),
		links:     map[config.UpstreamRung]*Link{},
		kick:      make(chan struct{}, 1),
	}
	c.HTTP = &http.Client{
		// A redirect is refused, not followed: curl and Go's client turn a
		// redirected POST into a GET with no body (the office's HAProxy
		// answers http with a 301 — redline hit this), and following a GET
		// across hosts drops the bearer token. Either way the fix is the URL.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return fmt.Errorf("the parent hz redirected to %s — set upstream.url to the URL that answers directly (a redirected POST loses its body)", req.URL)
		},
	}
	c.queue = &Queue{Dir: dir}
	return c
}

func (c *Child) loud(msg string, args ...any) { c.Log.Error("LOUD: "+msg, args...) }

// CheckToken is the start-up check: an unreadable token file is LOUD, never
// fatal — the cache still serves without the parent (#5).
func (c *Child) CheckToken() {
	if _, err := c.token(); err != nil {
		c.loud("nested hz cannot read its instance token; it will serve its cache and not reach the parent", "token_file", c.TokenFile, "error", err)
	}
}

func (c *Child) token() (string, error) {
	raw, err := os.ReadFile(c.TokenFile)
	if err != nil {
		return "", err
	}
	tok := strings.TrimSpace(string(raw))
	if tok == "" {
		return "", errors.New("the token file is empty")
	}
	return tok, nil
}

// Serves reports whether this child answers for a rung.
func (c *Child) Serves(project, environment string) bool {
	for _, r := range c.Rungs {
		if r.Project == project && r.Environment == environment {
			return true
		}
	}
	return false
}

// Run polls every rung and drains the report queue until ctx ends.
func (c *Child) Run(ctx context.Context) {
	c.CheckToken()
	t := time.NewTicker(c.Interval)
	defer t.Stop()
	for {
		c.PollOnce(ctx)
		_ = c.DrainOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-c.kick:
		}
	}
}

// Kick asks Run to poll and drain now (after a report is queued).
func (c *Child) Kick() {
	select {
	case c.kick <- struct{}{}:
	default:
	}
}

// PollOnce polls every configured rung once.
func (c *Child) PollOnce(ctx context.Context) {
	for _, r := range c.Rungs {
		if err := c.pollRung(ctx, r); err != nil {
			c.setLink(r, func(l *Link) { l.LastError = err.Error() })
		}
	}
	c.prune()
}

func (c *Child) link(r config.UpstreamRung) Link {
	c.mu.Lock()
	defer c.mu.Unlock()
	if l, ok := c.links[r]; ok {
		return *l
	}
	return Link{}
}

func (c *Child) setLink(r config.UpstreamRung, f func(*Link)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	l, ok := c.links[r]
	if !ok {
		l = &Link{}
		c.links[r] = l
	}
	f(l)
}

func now() string { return time.Now().UTC().Format(time.RFC3339) }

// pollRung is one conditional GET, decided by Next and acted on here.
func (c *Child) pollRung(ctx context.Context, r config.UpstreamRung) error {
	cache, cerr := c.LoadCache(r)
	if cerr != nil && !errors.Is(cerr, os.ErrNotExist) {
		// Unreadable is NOT missing (#11's rule): say so, and do not fetch over
		// a cache nobody can read.
		c.loud("nested hz cannot read its desired cache", "rung", r.String(), "error", cerr)
		return cerr
	}
	p := c.poll(ctx, r, cache)
	have := false
	if p.Kind == PollDesired && p.Desired != nil && artifact.Valid(p.Desired.ArtifactSHA256) {
		have, _ = c.Store.Has(p.Desired.ArtifactSHA256)
	}
	step := Next(r.String(), cache, p, have)
	if step.Loud != "" {
		c.loud(step.Loud)
	}
	switch p.Kind {
	case PollUnreachable:
		c.setLink(r, func(l *Link) {
			if l.UnreachableSince == "" {
				l.UnreachableSince = now()
			}
			l.RefusedSince, l.RefusedWhy = "", ""
			l.LastError = p.Err
		})
		return nil
	case PollRefused:
		c.setLink(r, func(l *Link) {
			if l.RefusedSince == "" {
				l.RefusedSince = now()
			}
			l.RefusedWhy = fmt.Sprintf("%d %s", p.Status, p.Err)
			l.UnreachableSince = ""
			l.LastError = l.RefusedWhy
		})
		return nil
	}
	c.setLink(r, func(l *Link) {
		l.UnreachableSince, l.RefusedSince, l.RefusedWhy = "", "", ""
		l.NothingApplied = p.Kind == PollNothingApplied && cache == nil
		l.LastError = ""
	})
	if step.Fetch != "" {
		if err := c.fetchArtifact(ctx, step.Fetch); err != nil {
			c.loud("nested hz did NOT cache the new desired state: its artifact failed", "rung", r.String(),
				"apply_id", p.Desired.ApplyID, "sha256", step.Fetch, "error", err)
			return err
		}
	}
	if step.WriteCache {
		next := Cache{Desired: *p.Desired, ETag: p.ETag, CachedAt: now()}
		if cache != nil && cache.Desired.ArtifactSHA256 != p.Desired.ArtifactSHA256 {
			next.PreviousSHA256 = cache.Desired.ArtifactSHA256
		} else if cache != nil {
			next.PreviousSHA256 = cache.PreviousSHA256
		}
		if err := c.writeCache(r, next); err != nil {
			c.loud("nested hz verified an artifact but could not write its cache", "rung", r.String(), "error", err)
			return err
		}
		c.Log.Info("nested hz cached a new desired state", "rung", r.String(), "apply_id", next.Desired.ApplyID,
			"version", next.Desired.Version, "sha256", next.Desired.ArtifactSHA256, "held", next.Desired.Hold != nil)
	}
	return nil
}

func (c *Child) request(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	tok, err := c.token()
	if err != nil {
		return nil, fmt.Errorf("instance token unreadable (%s): %w", c.TokenFile, err)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.URL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	hzapi.SetRequest(req.Header)
	return req, nil
}

func errorBody(resp *http.Response) string {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(raw, &e) == nil && e.Error != "" {
		return e.Error
	}
	return strings.TrimSpace(string(raw))
}

func (c *Child) poll(ctx context.Context, r config.UpstreamRung, cache *Cache) Poll {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	q := url.Values{"project": {r.Project}, "environment": {r.Environment}}
	req, err := c.request(ctx, http.MethodGet, "/api/v1/deploys/desired?"+q.Encode(), nil)
	if err != nil {
		// No token is not "unreachable": the parent was never asked. It reads
		// as a refusal with the reason, which is the operator's fix.
		return Poll{Kind: PollRefused, Err: err.Error()}
	}
	if cache != nil && cache.ETag != "" {
		req.Header.Set("If-None-Match", cache.ETag)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Poll{Kind: PollUnreachable, Err: err.Error()}
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusNotModified:
		return Poll{Kind: PollNotModified, Status: resp.StatusCode}
	case resp.StatusCode == http.StatusNotFound:
		return Poll{Kind: PollNothingApplied, Status: resp.StatusCode, Err: errorBody(resp)}
	case resp.StatusCode == http.StatusOK:
		var d apitypes.DesiredResp
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&d); err != nil {
			return Poll{Kind: PollUnreachable, Status: resp.StatusCode, Err: "unreadable desired body: " + err.Error()}
		}
		return Poll{Kind: PollDesired, Status: resp.StatusCode, Desired: &d, ETag: resp.Header.Get("ETag")}
	case resp.StatusCode >= 500:
		return Poll{Kind: PollUnreachable, Status: resp.StatusCode, Err: fmt.Sprintf("%d %s", resp.StatusCode, errorBody(resp))}
	default:
		return Poll{Kind: PollRefused, Status: resp.StatusCode, Err: errorBody(resp)}
	}
}

// fetchArtifact downloads sha from the parent into the store, verifying it.
// A body that does not hash to sha is refused by Store.Put: no file, no cache.
func (c *Child) fetchArtifact(ctx context.Context, sha string) error {
	ctx, cancel := context.WithTimeout(ctx, time.Hour)
	defer cancel()
	req, err := c.request(ctx, http.MethodGet, "/api/v1/artifacts/"+sha, nil)
	if err != nil {
		return err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("parent answered %d: %s", resp.StatusCode, errorBody(resp))
	}
	max := c.MaxArtifactBytes
	if max <= 0 {
		max = config.DefaultArtifactMaxBytes
	}
	n, err := c.Store.Put(resp.Body, sha, max)
	if err != nil {
		return err
	}
	c.Log.Info("nested hz pulled and verified an artifact", "sha256", sha, "size", n)
	return nil
}

func cacheName(r config.UpstreamRung) string {
	safe := func(s string) string {
		var b strings.Builder
		for _, ch := range s {
			if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '-' || ch == '_' || ch == '.' {
				b.WriteRune(ch)
			} else {
				b.WriteByte('_')
			}
		}
		return b.String()
	}
	sum := sha256.Sum256([]byte(r.Project + "\x00" + r.Environment))
	return "desired-" + safe(r.Project) + "--" + safe(r.Environment) + "-" + hex.EncodeToString(sum[:4]) + ".json"
}

// CachePath is where a rung's desired cache lives.
func (c *Child) CachePath(r config.UpstreamRung) string { return filepath.Join(c.Dir, cacheName(r)) }

// LoadCache reads a rung's cache. Missing is an error wrapping os.ErrNotExist;
// anything else is unreadable, which is a different answer.
func (c *Child) LoadCache(r config.UpstreamRung) (*Cache, error) {
	raw, err := os.ReadFile(c.CachePath(r))
	if err != nil {
		return nil, err
	}
	var cache Cache
	if err := json.Unmarshal(raw, &cache); err != nil {
		return nil, fmt.Errorf("%s: %w", c.CachePath(r), err)
	}
	return &cache, nil
}

// writeCache persists atomically, 0600.
func (c *Child) writeCache(r config.UpstreamRung, cache Cache) error {
	raw, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(c.CachePath(r), raw)
}

func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func (c *Child) caches() []*Cache {
	var out []*Cache
	for _, r := range c.Rungs {
		if cache, err := c.LoadCache(r); err == nil {
			out = append(out, cache)
		}
	}
	return out
}

// prune deletes pulled artifacts no cache names (decided by Prune).
func (c *Child) prune() {
	files, err := c.Store.List()
	if err != nil {
		return
	}
	for _, sha := range Prune(c.caches(), files) {
		if err := c.Store.Remove(sha); err != nil {
			c.loud("nested hz could not delete a pulled artifact no rung names", "sha256", sha, "error", err)
			continue
		}
		c.Log.Info("nested hz deleted a pulled artifact no rung names any more", "sha256", sha)
	}
}

// Answer is the local desired response for a rung.
func (c *Child) Answer(r config.UpstreamRung, ifNoneMatch string) Response {
	cache, err := c.LoadCache(r)
	link := c.link(r)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		c.loud("nested hz cannot read its desired cache", "rung", r.String(), "error", err)
		return Response{Status: 503, Error: "the desired cache for " + r.String() + " is unreadable: " + err.Error()}
	}
	resp := Answer(r.String(), cache, link, ifNoneMatch)
	if resp.Upstream != "" {
		c.loud("nested hz is serving a cached desired state without the parent", "rung", r.String(), "upstream", resp.Upstream)
	}
	return resp
}

// ArtifactRungs is every configured rung whose cache pins sha, current or
// previous — the local artifact endpoint's scope.
func (c *Child) ArtifactRungs(sha string) []config.UpstreamRung {
	var out []config.UpstreamRung
	for _, r := range c.Rungs {
		cache, err := c.LoadCache(r)
		if err == nil && ServesArtifact([]*Cache{cache}, sha) {
			out = append(out, r)
		}
	}
	return out
}

// Enqueue appends a report for the parent and wakes the drainer. It never
// waits on the parent.
func (c *Child) Enqueue(req apitypes.DeployReportReq) error {
	if err := c.queue.Append(req); err != nil {
		return err
	}
	c.Kick()
	return nil
}

// DrainOnce forwards every queued report, in order, until one fails.
func (c *Child) DrainOnce(ctx context.Context) error {
	return c.queue.Drain(func(req apitypes.DeployReportReq) (bool, error) {
		return c.forward(ctx, req)
	}, c.loud)
}

// forward POSTs one report with the instance token. retry=true leaves it at
// the head of the queue; retry=false with an error is a refusal the parent
// will repeat, which is set aside (kept, never dropped).
func (c *Child) forward(ctx context.Context, rep apitypes.DeployReportReq) (retry bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	raw, err := json.Marshal(rep)
	if err != nil {
		return false, err
	}
	req, err := c.request(ctx, http.MethodPost, "/api/v1/deploys/report", strings.NewReader(string(raw)))
	if err != nil {
		return true, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return true, err
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusOK:
		return false, nil
	case resp.StatusCode >= 500, resp.StatusCode == http.StatusUnauthorized,
		resp.StatusCode == http.StatusRequestTimeout, resp.StatusCode == http.StatusTooManyRequests:
		return true, fmt.Errorf("parent answered %d: %s", resp.StatusCode, errorBody(resp))
	default:
		return false, fmt.Errorf("parent refused the report %d: %s", resp.StatusCode, errorBody(resp))
	}
}

// QueueDepth is how many reports wait for the parent (for status and tests).
func (c *Child) QueueDepth() (int, error) { return c.queue.Pending() }
