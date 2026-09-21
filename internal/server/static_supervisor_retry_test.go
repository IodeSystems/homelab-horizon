package server

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// The finding: `static child exited  fork/exec …: permission denied` retried at
// 1s, 2s, 4s… forever. A misconfiguration that can never succeed must say so
// and stop, not loop — an endless retry makes an unfixable state look exactly
// like a service flapping.

func TestPermanentSpawnErrorClassifiesUnfixableLaunchFailures(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"permission denied", &spawnError{&fs.PathError{Op: "fork/exec", Err: syscall.EACCES}}, true},
		{"operation not permitted", &spawnError{&fs.PathError{Op: "fork/exec", Err: syscall.EPERM}}, true},
		{"binary gone", &spawnError{&fs.PathError{Op: "fork/exec", Err: syscall.ENOENT}}, true},
		{"not an executable", &spawnError{&fs.PathError{Op: "fork/exec", Err: syscall.ENOEXEC}}, true},
		// Transient: retrying is the right answer to these.
		{"out of memory at spawn", &spawnError{syscall.ENOMEM}, false},
		{"child exited non-zero", &exec.ExitError{}, false},
		{"child was killed", errors.New("signal: killed"), false},
		{"nil-ish unrelated error", errors.New("broken pipe"), false},
	}
	for _, tc := range cases {
		if got := permanentSpawnError(tc.err); got != tc.want {
			t.Errorf("%s: permanentSpawnError(%v) = %v, want %v", tc.name, tc.err, got, tc.want)
		}
	}
}

// A child that RAN and then exited — however badly — must never be mistaken
// for an impossible launch. Wrapping the wrong error here would turn a
// crash-looping file server into a silently disabled one.
func TestAnExitedChildIsNeverPermanent(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "exit 13")
	err := cmd.Run()
	if err == nil {
		t.Fatal("expected a non-zero exit")
	}
	if permanentSpawnError(err) {
		t.Fatal("a child that exited non-zero must be retried, not treated as unfixable")
	}
}

// End to end over a real fork/exec: a file the kernel refuses to execute
// produces an error the supervisor classifies as permanent.
func TestRealPermissionDeniedSpawnIsPermanent(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "notexecutable")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\ntrue\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin)
	err := cmd.Start()
	if err == nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Skip("this environment executed a 0600 file; nothing to classify")
	}
	if !permanentSpawnError(&spawnError{err}) {
		t.Fatalf("a real permission-denied fork/exec must be permanent; got %v", err)
	}
}

// The behaviour, not just the classifier: superviseChild must return rather
// than loop, and must record why.
func TestSuperviseChildStopsOnAPermanentSpawnFailure(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "notexecutable")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\ntrue\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if cmd := exec.Command(bin); cmd.Start() == nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Skip("this environment executed a 0600 file; nothing to classify")
	}

	s := newStaticSupervisor("127.0.0.1:0", false)
	done := make(chan struct{})
	go func() {
		defer close(done)
		// Own uid/gid: the point is the unreadable binary, not privilege
		// dropping, which a test cannot do.
		s.superviseChildFrom(bin, &syscall.Credential{
			Uid: uint32(os.Getuid()), Gid: uint32(os.Getgid()),
		})
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("superviseChild is still retrying a launch that can never succeed")
	}

	s.mu.Lock()
	failure := s.spawnFailure
	s.mu.Unlock()
	if failure == "" {
		t.Fatal("giving up must be recorded, not silent")
	}
}
