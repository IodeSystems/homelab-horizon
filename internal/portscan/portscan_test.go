package portscan

import (
	"context"
	"net"
	"testing"
	"time"
)

// The point of the package: a port with something on it is reported open, one
// without is not. Derived configuration cannot tell these apart, which is how
// the allocator handed out a port a running daemon already held.
func TestObserveDistinguishesListeningFromFree(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	busy := ln.Addr().(*net.TCPAddr).Port

	// A port nothing is on: bind then release, so it is genuinely unused.
	spare, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	free := spare.Addr().(*net.TCPAddr).Port
	_ = spare.Close()

	open := Observe(context.Background(), "127.0.0.1", []int{busy, free})

	if !open[busy] {
		t.Errorf("port %d has a listener and was reported free", busy)
	}
	if open[free] {
		t.Errorf("port %d has no listener and was reported open", free)
	}
}

// A scan must not become an unbounded favour to whoever asks.
func TestScansAreBounded(t *testing.T) {
	if got := len(Range(1, 70000)); got != MaxPorts {
		t.Fatalf("Range returned %d ports, want the %d cap", got, MaxPorts)
	}
	if got := Range(500, 400); got != nil {
		t.Fatalf("an inverted range should be empty, got %v", got)
	}
	if got := Range(20000, 20004); len(got) != 5 || got[0] != 20000 || got[4] != 20004 {
		t.Fatalf("Range(20000,20004) = %v", got)
	}

	many := make([]int, MaxPorts+500)
	for i := range many {
		many[i] = 1 + i
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Scanning a closed loopback range is fast; the cap is what is under test.
	_ = Observe(ctx, "127.0.0.1", many)
}

// A cancelled scan stops rather than running to completion.
func TestObserveHonoursCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := Observe(ctx, "127.0.0.1", Range(20000, 20100)); len(got) != 0 {
		t.Fatalf("a cancelled scan returned %d results", len(got))
	}
}
