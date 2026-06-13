package server

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/config"
)

// staticSupervisor manages the unprivileged static-file-server child process.
// It owns the current host->site map and pushes it to the child over a pipe on
// every config change; if the child dies it is respawned and re-seeded.
//
// The point is privilege separation: hz runs as root, but files must never be
// served by a root process. In production the supervisor spawns a child dropped
// to an unprivileged user. In dev (not root) there is nothing to drop, so it
// serves in-process. If it is root but cannot drop, it refuses to serve rather
// than serve files as root.
type staticSupervisor struct {
	addr   string
	dryRun bool

	mu      sync.Mutex
	sites   map[string]staticSite
	stdin   io.WriteCloser // pipe to the running child; nil when none
	stopped bool
	inproc  *staticServer // set only in the in-process (dev) fallback
}

func newStaticSupervisor(addr string, dryRun bool) *staticSupervisor {
	return &staticSupervisor{addr: addr, dryRun: dryRun, sites: map[string]staticSite{}}
}

// Rebuild stores the latest map and pushes it to a running child / in-proc server.
func (s *staticSupervisor) Rebuild(cfg *config.Config) {
	sites := deriveStaticSites(cfg)
	s.mu.Lock()
	s.sites = sites
	w := s.stdin
	inproc := s.inproc
	s.mu.Unlock()

	if inproc != nil {
		inproc.setSites(sites)
	}
	if w != nil {
		s.push(w, sites)
	}
}

func (s *staticSupervisor) push(w io.Writer, sites map[string]staticSite) {
	b, err := json.Marshal(sites)
	if err != nil {
		slog.Warn("static: marshal sites", "err", err)
		return
	}
	if _, err := w.Write(append(b, '\n')); err != nil {
		slog.Warn("static: push to child failed", "err", err)
	}
}

// Start launches the static file server according to the environment:
//   - dry-run: no-op.
//   - not root: serve in-process (already unprivileged — dev/test).
//   - root: spawn an unprivileged child. If privileges can't be dropped, refuse
//     to serve (fail closed) rather than serve files as root.
func (s *staticSupervisor) Start() {
	if s.dryRun {
		return
	}
	if os.Geteuid() != 0 {
		s.startInProcess()
		return
	}
	cred, err := unprivilegedCredential()
	if err != nil {
		slog.Error("static: cannot drop privileges — static services DISABLED (refusing to serve files as root)", "err", err)
		return
	}
	go s.superviseChild(cred)
}

func (s *staticSupervisor) startInProcess() {
	ss := newStaticServer()
	s.mu.Lock()
	ss.setSites(s.sites)
	s.inproc = ss
	s.mu.Unlock()

	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		slog.Warn("static: in-process bind failed, static services disabled", "addr", s.addr, "err", err)
		return
	}
	srv := &http.Server{Addr: s.addr, Handler: ss, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
	slog.Warn("static file server running in-process (not root; dev mode)", "addr", s.addr)
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			slog.Warn("static in-process server stopped", "err", err)
		}
	}()
}

// superviseChild (re-)spawns the child and restarts it with backoff if it exits
// unexpectedly. Each spawn is re-seeded with the current map.
func (s *staticSupervisor) superviseChild(cred *syscall.Credential) {
	exe, err := os.Executable()
	if err != nil {
		slog.Error("static: cannot resolve own executable", "err", err)
		return
	}
	backoff := time.Second
	for {
		s.mu.Lock()
		stopped := s.stopped
		s.mu.Unlock()
		if stopped {
			return
		}

		start := time.Now()
		if err := s.runChildOnce(exe, cred); err != nil {
			slog.Warn("static child exited", "err", err)
		}

		// Back off only on rapid failure to avoid a respawn spin.
		if time.Since(start) < 2*time.Second {
			time.Sleep(backoff)
			if backoff < 30*time.Second {
				backoff *= 2
			}
		} else {
			backoff = time.Second
		}
	}
}

func (s *staticSupervisor) runChildOnce(exe string, cred *syscall.Credential) error {
	cmd := exec.Command(exe)
	// Minimal env — don't leak the parent's environment (it may hold secrets)
	// into the less-trusted child.
	cmd.Env = []string{StaticServerEnvAddr + "=" + s.addr}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: cred}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return err
	}
	slog.Info("static file server child started", "pid", cmd.Process.Pid, "uid", cred.Uid, "addr", s.addr)

	// Register the pipe and seed the child with the current map.
	s.mu.Lock()
	s.stdin = stdin
	sites := s.sites
	s.mu.Unlock()
	s.push(stdin, sites)

	err = cmd.Wait()

	s.mu.Lock()
	s.stdin = nil
	s.mu.Unlock()
	_ = stdin.Close()
	return err
}

// Stop terminates the child (if any) and prevents further restarts. Closing the
// child's stdin makes it shut its listener down and exit.
func (s *staticSupervisor) Stop() {
	s.mu.Lock()
	s.stopped = true
	w := s.stdin
	s.stdin = nil
	s.mu.Unlock()
	if w != nil {
		_ = w.Close()
	}
}

// unprivilegedCredential resolves the "nobody" user to a process credential for
// the child.
func unprivilegedCredential() (*syscall.Credential, error) {
	u, err := user.Lookup("nobody")
	if err != nil {
		return nil, fmt.Errorf("lookup user nobody: %w", err)
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return nil, fmt.Errorf("parse nobody uid %q: %w", u.Uid, err)
	}
	gid, err := strconv.Atoi(u.Gid)
	if err != nil {
		return nil, fmt.Errorf("parse nobody gid %q: %w", u.Gid, err)
	}
	return &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)}, nil
}
