package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/wireguard"
)

func testWGEnv(t *testing.T, euid int) (wgCreateConfigEnv, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "wireguard", "wg0.conf")
	return wgCreateConfigEnv{
		loadConfig: func() (*config.Config, error) {
			return &config.Config{WGConfigPath: path, VPNRange: "10.0.2.0/24", WGInterface: "wg0"}, nil
		},
		defaultIface: func() string { return "eth0" },
		mintKey: func() (string, string, error) {
			return "PRIVATE-KEY-AAAAAAAAAAAAAAAAAAAAAAAAAAAA=", "PUBLIC-KEY-AAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", nil
		},
		euid: euid,
	}, path
}

func TestWGCreateConfigWritesAFreshInterface(t *testing.T) {
	env, path := testWGEnv(t, 0)

	var out bytes.Buffer
	if err := env.create(&out, false); err != nil {
		t.Fatalf("create: %v\n%s", err, out.String())
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("no config was written: %v", err)
	}
	parsed, err := wireguard.ParseConfig(body)
	if err != nil {
		t.Fatalf("what it wrote does not parse: %v", err)
	}
	if parsed.Address != "10.0.2.1/24" {
		t.Errorf("Address = %q, want 10.0.2.1/24", parsed.Address)
	}
	if parsed.ListenPort != "51820" {
		t.Errorf("ListenPort = %q", parsed.ListenPort)
	}
	if !strings.Contains(parsed.PostUp, "eth0") {
		t.Errorf("PostUp does not NAT out of the default interface: %q", parsed.PostUp)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("wg0.conf mode %04o, want 0600 — it holds the server private key", fi.Mode().Perm())
	}
	if di, err := os.Stat(filepath.Dir(path)); err != nil {
		t.Fatal(err)
	} else if di.Mode().Perm() != 0o700 {
		t.Errorf("/etc/wireguard mode %04o, want 0700", di.Mode().Perm())
	}

	// The public half is what the operator needs; the private half must not be
	// on their terminal, in their scrollback or in their shell's log.
	s := out.String()
	if !strings.Contains(s, "PUBLIC-KEY-") {
		t.Errorf("the public key must be printed; got:\n%s", s)
	}
	if strings.Contains(s, "PRIVATE-KEY-") {
		t.Errorf("THE PRIVATE KEY WAS PRINTED:\n%s", s)
	}
}

// THE REFUSAL (privilege-audit.md §3.1 #5). Regenerating over a live tunnel
// mints a new server identity and drops every peer, and the file being there
// is the only warning anyone would get.
func TestWGCreateConfigRefusesWhenTheConfigExists(t *testing.T) {
	env, path := testWGEnv(t, 0)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	const existing = "[Interface]\nPrivateKey = THE-LIVE-SERVER-KEY\n"
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	err := env.create(&out, false)
	if err == nil {
		t.Fatal("it replaced a live wg0.conf")
	}
	if got, _ := os.ReadFile(path); string(got) != existing {
		t.Fatalf("the existing config was modified:\n%s", got)
	}
	msg := err.Error()
	for _, want := range []string{"already exists", "server private key", "mv "} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal must name %q — it has to say what the consequence is and\n"+
				"what to do instead; got:\n%s", want, msg)
		}
	}
	// It says there is no escape hatch, and there genuinely is none: an
	// operator told "refusing" with no explanation goes looking for the flag.
	if !strings.Contains(msg, "no --force") {
		t.Errorf("the refusal must say there is no --force, so nobody goes hunting for one; got:\n%s", msg)
	}
	src, err := os.ReadFile("wgconfig.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(src), `fset.Bool("force"`) || strings.Contains(string(src), `fset.Bool("overwrite"`) {
		t.Error("the verb grew an override flag; the refusal is the feature, and a flag is not a backup")
	}
}

// Unreadable is not absent. A stat that fails for any other reason must refuse
// rather than treat the path as free.
func TestWGCreateConfigRefusesWhenItCannotTell(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root traverses a 0000 directory")
	}
	env, path := testWGEnv(t, 0)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	var out bytes.Buffer
	err := env.create(&out, false)
	if err == nil {
		t.Fatal("an unreadable path must not be treated as an absent one")
	}
	if !strings.Contains(err.Error(), "cannot tell") {
		t.Errorf("the refusal must say it could not tell; got %v", err)
	}
}

// O_EXCL is the guarantee; the stat in create() is only the message.
//
// This drives writeNewFile DIRECTLY, because through create() the stat refuses
// first and the write is never reached — a version of this test that called
// create() twice stayed green with O_EXCL swapped for O_TRUNC, which is a test
// pinned exactly where the property was already enforced by something else.
// Measured, then fixed; the control is in the commit message.
func TestTheWriteItselfIsExclusive(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wg0.conf")
	const existing = "[Interface]\nPrivateKey = THE-LIVE-SERVER-KEY\n"
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := writeNewFile(path, "[Interface]\nPrivateKey = A-BRAND-NEW-ONE\n"); err == nil {
		t.Fatal("the write replaced an existing wg0.conf; only the kernel can close " +
			"the window between the stat and the write, and O_EXCL is how")
	}
	if got, _ := os.ReadFile(path); string(got) != existing {
		t.Fatalf("the existing config was modified:\n%s", got)
	}

	// The positive half: on a free path it writes, 0600.
	fresh := filepath.Join(dir, "new.conf")
	if err := writeNewFile(fresh, "body"); err != nil {
		t.Fatalf("writeNewFile on a free path: %v", err)
	}
	fi, err := os.Stat(fresh)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %04o, want 0600", fi.Mode().Perm())
	}
}

func TestWGCreateConfigDryRunWritesNothing(t *testing.T) {
	env, path := testWGEnv(t, 1000)

	var out bytes.Buffer
	if err := env.create(&out, true); err != nil {
		t.Fatalf("a dry run must not need root: %v", err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("DRY RUN wrote the config")
	}
	for _, want := range []string{"DRY RUN", "10.0.2.1/24", "51820", "eth0"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the dry run must name %q; got:\n%s", want, out.String())
		}
	}
}

func TestWGCreateConfigNeedsRoot(t *testing.T) {
	env, path := testWGEnv(t, 1000)

	var out bytes.Buffer
	err := env.create(&out, false)
	if err == nil {
		t.Fatal("writing the server's private key must need root")
	}
	if !strings.Contains(err.Error(), "sudo hz-agent wg-create-config") {
		t.Errorf("the refusal must name the command to run; got %v", err)
	}
	if _, statErr := os.Stat(path); statErr == nil {
		t.Fatal("it wrote the file anyway")
	}
}

// A VPN range hz cannot address is an error BEFORE a key exists, not a config
// written from a guess.
func TestWGCreateConfigRefusesAnUnusableRange(t *testing.T) {
	env, path := testWGEnv(t, 0)
	env.loadConfig = func() (*config.Config, error) {
		return &config.Config{WGConfigPath: path, VPNRange: "not-a-range"}, nil
	}
	minted := false
	env.mintKey = func() (string, string, error) { minted = true; return "", "", nil }

	if err := env.create(&bytes.Buffer{}, false); err == nil {
		t.Fatal("an unusable VPN range must be an error")
	}
	if minted {
		t.Error("a key was minted for a config that could never be written")
	}
}

// The verb is dispatched. A file full of correct code that main() never calls
// is not a verb.
func TestWGCreateConfigIsDispatched(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	if !strings.Contains(s, `case "wg-create-config":`) {
		t.Error("main.go does not dispatch wg-create-config")
	}
	if !strings.Contains(s, "wg-create-config  Write a fresh wg0.conf") {
		t.Error("the usage does not list wg-create-config; an undiscoverable verb is not a remedy")
	}
}
