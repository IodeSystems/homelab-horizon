package configmgr

import (
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"testing"
)

// The project coordinate exists to refuse one specific read, and this file is
// the standing proof that it does.
//
// Before the project joined the address, a config address was
// (environment, app, role). Environment names are unique per project and not
// globally, so `prod/redline/app` named a single thing only while exactly one
// project declared an app called `redline`. Two projects that both did shared
// an address. On its own that is a collision rather than a disclosure, because
// an environment key is minted per address — but `hz config key import` exists
// precisely so one key can sit at several addresses, and once it does the
// authenticated additional data is the only thing between the two projects.
//
// Run against the three-field context, this test's first two cases PASS the
// open: acme's production password came back as plaintext under globex's
// address, and acme's wrapped environment key unwrapped under globex's grant
// address. That was measured before the change, not assumed.
//
// Nothing below may be relaxed to make a build green. A failure here means the
// cross-project read is reachable again.

// sameTripleDifferentProjects is the exact collision: two projects whose rungs
// agree in every field the old address had.
func sameTripleDifferentProjects() (acme, globex Addr) {
	acme = Addr{Project: "acme", Environment: "prod", App: "redline", Role: "app", Key: "DB_PASSWORD"}
	globex = Addr{Project: "globex", Environment: "prod", App: "redline", Role: "app", Key: "DB_PASSWORD"}
	return acme, globex
}

func TestCrossProjectReadIsRefused(t *testing.T) {
	acme, globex := sameTripleDifferentProjects()

	// One key, installed at both addresses — what `hz config key import` does.
	// Every other defence is therefore already spent: same key, same key id,
	// same envelope kind, same environment, app, role and key name. The
	// additional data is all that is left.
	k := NewEnvKey()
	secret := []byte("acme's production password")
	blob := Seal(k, acme, secret)

	pt, err := Open(k, globex, blob)
	if err == nil {
		t.Fatalf("a kind 0x01 value sealed at %s opened at %s, plaintext %q", acme, globex, pt)
	}
	if err != ErrAuthentication {
		t.Fatalf("want ErrAuthentication, got %v", err)
	}

	// The refusal is the address, not the key: acme's own address still opens.
	got, err := Open(k, acme, blob)
	if err != nil {
		t.Fatalf("the owning address must still open its own value: %v", err)
	}
	if !bytes.Equal(got, secret) {
		t.Fatalf("plaintext = %q, want %q", got, secret)
	}
}

func TestCrossProjectGrantIsRefused(t *testing.T) {
	acme, globex := sameTripleDifferentProjects()

	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	k := NewEnvKey()

	// The grant is wrapped to acme's rung. Right recipient, right ECDH, right
	// kind — and it must still refuse to unwrap as globex's.
	wrapped, err := WrapEnvKey(priv.PublicKey(), acme.EnvKeyAddr(), k)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := UnwrapEnvKey(priv, globex.EnvKeyAddr(), wrapped); err == nil {
		t.Fatalf("a kind 0x02 grant made at %s unwrapped at %s", acme.EnvKeyAddr(), globex.EnvKeyAddr())
	}

	got, err := UnwrapEnvKey(priv, acme.EnvKeyAddr(), wrapped)
	if err != nil {
		t.Fatalf("the owning address must still unwrap its own grant: %v", err)
	}
	if got != k {
		t.Fatal("unwrapped a different key")
	}
}

// TestProjectIsBoundAcrossEveryEnvelopeKind states the whole rule in one place:
// the project is authenticated on kinds 0x01 and 0x02, and kind 0x03 has no
// project field at all because a machine id is globally unique.
func TestProjectIsBoundAcrossEveryEnvelopeKind(t *testing.T) {
	acme, globex := sameTripleDifferentProjects()

	t.Run("kind 0x01 env-sealed", func(t *testing.T) {
		k := NewEnvKey()
		if _, err := Open(k, globex, Seal(k, acme, []byte("v"))); err == nil {
			t.Fatal("opened across projects")
		}
	})

	t.Run("kind 0x02 wrapped-env-key", func(t *testing.T) {
		priv, err := ecdh.P256().GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		k := NewEnvKey()
		wrapped, err := WrapEnvKey(priv.PublicKey(), acme.EnvKeyAddr(), k)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := UnwrapEnvKey(priv, globex.EnvKeyAddr(), wrapped); err == nil {
			t.Fatal("unwrapped across projects")
		}
	})

	t.Run("kind 0x03 machine-sealed is untouched", func(t *testing.T) {
		priv, err := ecdh.P256().GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		// A machine-scoped secret is addressed by (machine id, key name). The
		// project change adds no field here, so a blob minted before it still
		// opens — which is what makes the flag day bounded rather than total.
		addr := MachineAddr{Machine: "m-1", Key: "TOKEN"}
		blob, err := SealToMachine(priv.PublicKey(), addr, []byte("v"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := OpenFromMachine(priv, addr, blob); err != nil {
			t.Fatalf("machine-scoped secrets must be unaffected by the project coordinate: %v", err)
		}
	})
}

// TestContextNamesTheProject pins the encoding itself. The browser reproduces
// these bytes by hand from configmgr/doc.go, so a context that silently loses
// the project field is a wrong implementation everywhere at once, and one that
// merely reorders it is a flag day nobody declared.
func TestContextNamesTheProject(t *testing.T) {
	addr := Addr{Project: "acme", Environment: "prod", App: "redline", Role: "app", Key: "DB_PASSWORD"}

	want := canonicalContext(labelAddr, "acme", "prod", "redline", "app", "DB_PASSWORD")
	if !bytes.Equal(addr.context(), want) {
		t.Fatalf("Addr context = % x\nwant                % x", addr.context(), want)
	}
	if got, want := addr.String(), "acme/prod/redline/app#DB_PASSWORD"; got != want {
		t.Fatalf("Addr.String() = %q, want %q", got, want)
	}

	ek := addr.EnvKeyAddr()
	wantEK := canonicalContext(labelEnvKeyAddr, "acme", "prod", "redline", "app")
	if !bytes.Equal(ek.context(), wantEK) {
		t.Fatalf("EnvKeyAddr context = % x\nwant                     % x", ek.context(), wantEK)
	}
	if got, want := ek.String(), "acme/prod/redline/app"; got != want {
		t.Fatalf("EnvKeyAddr.String() = %q, want %q", got, want)
	}

	// The project is the FIRST field after the label, not an appended one.
	// Appending would leave the three-field prefix intact, which is the shape a
	// well-meaning "just add the field" patch produces.
	if !bytes.HasPrefix(addr.context(), appendField(appendField(nil, labelAddr), "acme")) {
		t.Fatal("the project must be the first field after the label")
	}
}

// TestThreeFieldContextDoesNotOpenAFourFieldSeal is the standing guard on the
// flag day. The old encoding is reconstructed here by hand — it no longer
// exists in the package — so that a future change which drops the project back
// out of the context fails loudly rather than quietly restoring the hole.
func TestThreeFieldContextDoesNotOpenAFourFieldSeal(t *testing.T) {
	k := NewEnvKey()
	addr := Addr{Project: "acme", Environment: "prod", App: "redline", Role: "app", Key: "DB_PASSWORD"}
	blob := Seal(k, addr, []byte("acme's production password"))

	// The pre-project context: label, environment, app, role, key name.
	old := canonicalContext(labelAddr, addr.Environment, addr.App, addr.Role, addr.Key)
	if bytes.Equal(old, addr.context()) {
		t.Fatal("the four-field context equals the old three-field one; the project is not bound")
	}

	nonce := blob[envHeaderLen : envHeaderLen+NonceSize]
	if _, err := mustGCM(k[:]).Open(nil, nonce, blob[envHeaderLen+NonceSize:], aad(blob[:envHeaderLen], old)); err == nil {
		t.Fatal("a value sealed under the four-field context opened under the old three-field one")
	}

	// Same for kind 0x02.
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	wrapped, err := WrapEnvKey(priv.PublicKey(), addr.EnvKeyAddr(), k)
	if err != nil {
		t.Fatal(err)
	}
	oldEK := canonicalContext(labelEnvKeyAddr, addr.Environment, addr.App, addr.Role)
	if _, err := openFrom(priv, KindWrappedEnvKey, purposeWrapEnvKey, oldEK, wrapped); err == nil {
		t.Fatal("a grant wrapped under the four-field context unwrapped under the old three-field one")
	}
}
