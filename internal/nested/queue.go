package nested

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
)

// Queue is the child's report queue: deploy reports received locally, waiting
// to be forwarded to the parent. NEVER DROPS ONE.
//
//	reports.queue.jsonl    append-only, one JSON report per line
//	reports.sent           how many lines have been delivered (atomic write)
//	reports.refused.jsonl  append-only: reports the parent refused for good
//	                       (a 4xx it will repeat), set aside so one bad report
//	                       cannot block every later one — kept, not dropped
//
// A report delivered but whose answer was lost is sent again: the parent then
// holds two identical rows. Reports are append-only observations and the
// newest is what any gate reads, so a duplicate is harmless; a lost one is
// not.
type Queue struct {
	Dir     string
	mu      sync.Mutex // the queue file: Append, and a read of it
	drainMu sync.Mutex // one drain at a time: the cursor and refused file
}

func (q *Queue) path(name string) string { return filepath.Join(q.Dir, name) }

// Append adds one report, fsynced before it returns.
func (q *Queue) Append(rep apitypes.DeployReportReq) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.appendLine("reports.queue.jsonl", rep)
}

func (q *Queue) appendLine(name string, rep apitypes.DeployReportReq) error {
	if err := os.MkdirAll(q.Dir, 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(rep)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(q.path(name), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(raw, '\n')); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func (q *Queue) lines() ([]string, error) {
	f, err := os.Open(q.path("reports.queue.jsonl"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var out []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" {
			out = append(out, line)
		}
	}
	return out, sc.Err()
}

func (q *Queue) sent() (int, error) {
	raw, err := os.ReadFile(q.path("reports.sent"))
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || n < 0 {
		return 0, fmt.Errorf("reports.sent is unreadable (%q) — refusing to guess which reports were delivered", strings.TrimSpace(string(raw)))
	}
	return n, nil
}

// Pending is how many reports have not been delivered or set aside.
func (q *Queue) Pending() (int, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	lines, err := q.lines()
	if err != nil {
		return 0, err
	}
	sent, err := q.sent()
	if err != nil {
		return 0, err
	}
	if sent > len(lines) {
		return 0, nil
	}
	return len(lines) - sent, nil
}

// Drain sends each pending report in order. send answers (retry, err): nil is
// delivered; retry stops the drain with the report still at the head; an
// error without retry sets the report aside in reports.refused.jsonl.
//
// The append lock is held only while the queue file is read, never across a
// send: an Append — the local report's answer — must not wait on the parent.
func (q *Queue) Drain(send func(apitypes.DeployReportReq) (bool, error), loud func(string, ...any)) error {
	q.drainMu.Lock()
	defer q.drainMu.Unlock()
	q.mu.Lock()
	lines, err := q.lines()
	q.mu.Unlock()
	if err != nil {
		loud("nested hz cannot read its report queue", "error", err)
		return err
	}
	sent, err := q.sent()
	if err != nil {
		loud("nested hz cannot read its report cursor", "error", err)
		return err
	}
	for i := sent; i < len(lines); i++ {
		var rep apitypes.DeployReportReq
		if err := json.Unmarshal([]byte(lines[i]), &rep); err != nil {
			loud("nested hz report queue holds a line it cannot read; setting it aside", "line", i+1, "error", err)
			if err := q.appendRaw("reports.refused.jsonl", lines[i]); err != nil {
				return err
			}
		} else if retry, err := send(rep); err != nil {
			if retry {
				loud("nested hz could not forward a deploy report to the parent; it stays queued", "pending", len(lines)-i,
					"rung", rep.Project+"/"+rep.Environment, "version", rep.Version, "error", err)
				return err
			}
			loud("the parent hz REFUSED a forwarded deploy report; it is kept in reports.refused.jsonl",
				"rung", rep.Project+"/"+rep.Environment, "version", rep.Version, "error", err)
			if err := q.appendLine("reports.refused.jsonl", rep); err != nil {
				return err
			}
		}
		if err := writeAtomic(q.path("reports.sent"), []byte(strconv.Itoa(i+1)+"\n")); err != nil {
			loud("nested hz could not advance its report cursor; the report may be sent twice", "error", err)
			return err
		}
	}
	return nil
}

func (q *Queue) appendRaw(name, line string) error {
	f, err := os.OpenFile(q.path(name), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(line + "\n"); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
