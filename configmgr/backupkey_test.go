package configmgr

import (
	"bytes"
	"crypto/rand"
	"errors"
	"testing"
)

func TestBackupKeyWrapRoundTrip(t *testing.T) {
	priv, err := NewMachineKey()
	if err != nil {
		t.Fatal(err)
	}
	key := make([]byte, BackupKeySize)
	_, _ = rand.Read(key)
	addr := BackupAddr{BackupID: "0123456789abcdef0123456789abcdef"}
	blob, err := WrapBackupKey(priv.PublicKey(), addr, key)
	if err != nil {
		t.Fatal(err)
	}
	h, err := ParseEnvelopeHeader(blob)
	if err != nil {
		t.Fatal(err)
	}
	if h.Kind != KindBackupKey || h.Recipient != FingerprintOf(priv.PublicKey()) {
		t.Fatalf("header %+v", h)
	}
	got, err := UnwrapBackupKey(priv, addr, blob)
	if err != nil || !bytes.Equal(got, key) {
		t.Fatalf("unwrap: %v", err)
	}

	// Positive control above; each of these differs in exactly one thing.
	if _, err := UnwrapBackupKey(priv, BackupAddr{BackupID: "other"}, blob); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("another backup's address: %v", err)
	}
	other, _ := NewMachineKey()
	if _, err := UnwrapBackupKey(other, addr, blob); !errors.Is(err, ErrKeyMismatch) {
		t.Fatalf("another recipient: %v", err)
	}
	if _, err := WrapBackupKey(priv.PublicKey(), addr, key[:16]); !errors.Is(err, ErrMalformedKey) {
		t.Fatalf("short key: %v", err)
	}
}

// A backup key and an environment key are both 32 bytes wrapped to a P-256
// key. The kind byte and the HKDF purpose keep either from being accepted as
// the other, in both directions.
func TestBackupKeyIsNotAnEnvKey(t *testing.T) {
	priv, err := NewMachineKey()
	if err != nil {
		t.Fatal(err)
	}
	k := NewEnvKey()
	envAddr := EnvKeyAddr{Project: "p", Environment: "e", App: "a", Role: "r"}
	wrappedEnv, err := WrapEnvKey(priv.PublicKey(), envAddr, k)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := UnwrapBackupKey(priv, BackupAddr{BackupID: "x"}, wrappedEnv); err == nil {
		t.Fatal("an environment key wrap opened as a backup key")
	}
	wrappedBackup, err := WrapBackupKey(priv.PublicKey(), BackupAddr{BackupID: "x"}, k[:])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := UnwrapEnvKey(priv, envAddr, wrappedBackup); err == nil {
		t.Fatal("a backup key wrap opened as an environment key")
	}
	// Relabelling the kind byte is caught by the AAD and the purpose.
	relabelled := append([]byte(nil), wrappedBackup...)
	relabelled[1] = byte(KindWrappedEnvKey)
	if _, err := UnwrapEnvKey(priv, envAddr, relabelled); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("relabelled: %v", err)
	}
}
