package offsite

import (
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/iodesystems/homelab-horizon/configmgr"
	"github.com/iodesystems/homelab-horizon/internal/config"
)

func testKey(t *testing.T) *ecdh.PrivateKey {
	t.Helper()
	priv, err := configmgr.NewMachineKey()
	if err != nil {
		t.Fatal(err)
	}
	return priv
}

func testMeta() Meta {
	return Meta{HZVersion: "1.2.3-test", Host: "gw.example.com", SchemaVersion: 42,
		SnapshotAt: time.Date(2026, 10, 9, 3, 0, 0, 0, time.UTC)}
}

func encryptTo(t *testing.T, plaintext []byte, privs ...*ecdh.PrivateKey) []byte {
	t.Helper()
	var rs []Recipient
	for i, p := range privs {
		rs = append(rs, Recipient{Name: []string{"offline-a", "offline-b", "offline-c"}[i], PublicKey: p.PublicKey()})
	}
	var buf bytes.Buffer
	if _, err := Encrypt(&buf, bytes.NewReader(plaintext), testMeta(), rs); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	a, b := testKey(t), testKey(t)
	for _, n := range []int{0, 1, ChunkSize - 1, ChunkSize, ChunkSize + 1, 2 * ChunkSize, 2*ChunkSize + 7} {
		pt := make([]byte, n)
		_, _ = rand.Read(pt)
		ct := encryptTo(t, pt, a, b)
		for name, k := range map[string]*ecdh.PrivateKey{"first": a, "second": b} {
			var out bytes.Buffer
			h, err := Decrypt(bytes.NewReader(ct), k, &out)
			if err != nil {
				t.Fatalf("%d bytes, %s recipient: %v", n, name, err)
			}
			if !bytes.Equal(out.Bytes(), pt) {
				t.Fatalf("%d bytes, %s recipient: plaintext differs", n, name)
			}
			if h.Meta != testMeta() || len(h.Recipients) != 2 {
				t.Fatalf("header %+v", h)
			}
		}
	}
}

func TestDecryptWithTheWrongKeyFails(t *testing.T) {
	ct := encryptTo(t, []byte("hello"), testKey(t))
	var out bytes.Buffer
	if _, err := Decrypt(bytes.NewReader(ct), testKey(t), &out); !errors.Is(err, ErrNotForThisKey) {
		t.Fatalf("got %v", err)
	}
	if out.Len() != 0 {
		t.Fatal("wrote plaintext for the wrong key")
	}
}

// Every way the file can be altered fails GCM authentication. The untouched
// file decrypting (the round-trip test, and the first case here) is the
// positive control.
func TestTamperedBackupFails(t *testing.T) {
	k := testKey(t)
	pt := make([]byte, 2*ChunkSize+100)
	_, _ = rand.Read(pt)
	ct := encryptTo(t, pt, k)
	_, preamble, err := ReadHeader(bytes.NewReader(ct))
	if err != nil {
		t.Fatal(err)
	}
	body := len(preamble)
	full := ChunkSize + tagSize

	cases := map[string]func([]byte) []byte{
		"untouched": func(b []byte) []byte { return b },
		"a ciphertext byte flipped": func(b []byte) []byte {
			b[body+full+10] ^= 0x01
			return b
		},
		"a tag byte flipped": func(b []byte) []byte {
			b[len(b)-1] ^= 0x01
			return b
		},
		"cut at a chunk boundary (last chunk dropped)": func(b []byte) []byte { return b[:body+2*full] },
		"cut mid-chunk":  func(b []byte) []byte { return b[:body+full+500] },
		"bytes appended": func(b []byte) []byte { return append(b, 0) },
		"two chunks swapped": func(b []byte) []byte {
			c := append([]byte(nil), b...)
			copy(c[body:body+full], b[body+full:body+2*full])
			copy(c[body+full:body+2*full], b[body:body+full])
			return c
		},
		"the header edited (same length)": func(b []byte) []byte {
			return bytes.Replace(b, []byte("gw.example.com"), []byte("gx.example.com"), 1)
		},
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			_, err := Decrypt(bytes.NewReader(edit(append([]byte(nil), ct...))), k, &out)
			if name == "untouched" {
				if err != nil || !bytes.Equal(out.Bytes(), pt) {
					t.Fatalf("positive control: %v", err)
				}
				return
			}
			if !errors.Is(err, ErrAuthentication) {
				t.Fatalf("got %v, want ErrAuthentication", err)
			}
		})
	}
}

func TestRefusesWithNoRecipients(t *testing.T) {
	if _, err := Encrypt(&bytes.Buffer{}, strings.NewReader("x"), testMeta(), nil); !errors.Is(err, ErrNoRecipients) {
		t.Fatalf("Encrypt: %v", err)
	}
	if _, err := RecipientsFromConfig(&config.Config{}); !errors.Is(err, ErrNoRecipients) {
		t.Fatalf("RecipientsFromConfig: %v", err)
	}
	if !strings.Contains(ErrNoRecipients.Error(), "hz config recovery keygen") {
		t.Fatal("the refusal does not say how to fix it")
	}
	if _, err := Push(t.Context(), &fakeAPI{}, Dest{Bucket: "example-hz-backups"}, []byte("x"), testMeta(), nil, time.Now(), t.TempDir()); !errors.Is(err, ErrNoRecipients) {
		t.Fatalf("Push: %v", err)
	}
}

func TestRecipientsFromConfigRefusesABadKey(t *testing.T) {
	good := configmgr.MarshalMachinePublicKey(testKey(t).PublicKey())
	cfg := &config.Config{RecoveryRecipients: []config.RecoveryRecipient{
		{Name: "offline-a", PublicKey: good},
		{Name: "offline-b", PublicKey: "hzmpub_bm90IGEga2V5"},
	}}
	if _, err := RecipientsFromConfig(cfg); err == nil || !strings.Contains(err.Error(), "offline-b") {
		t.Fatalf("got %v", err)
	}
}

func jsonKeys(t *testing.T, typ reflect.Type) []string {
	t.Helper()
	var keys []string
	for i := 0; i < typ.NumField(); i++ {
		tag := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
		if tag == "" || tag == "-" {
			t.Fatalf("%s.%s has no JSON name", typ.Name(), typ.Field(i).Name)
		}
		keys = append(keys, tag)
	}
	sort.Strings(keys)
	return keys
}

// The sidecar is uploaded in the clear. Its exact field list is pinned so a
// field cannot be added to it without this test being edited — and whoever
// edits it is looking at a list that says "no secrets". The wrapped keys are
// in the object, not here.
func TestTheSidecarCarriesNoSecrets(t *testing.T) {
	want := []string{"backup_id", "ciphertext_bytes", "ciphertext_sha256", "created_at", "format",
		"host", "hz_version", "object", "recipients", "schema_version", "snapshot_at"}
	if got := jsonKeys(t, reflect.TypeOf(Sidecar{})); !reflect.DeepEqual(got, want) {
		t.Fatalf("Sidecar fields %v, want exactly %v", got, want)
	}
	if got := jsonKeys(t, reflect.TypeOf(RecipientRef{})); !reflect.DeepEqual(got, []string{"fingerprint", "name"}) {
		t.Fatalf("RecipientRef fields %v", got)
	}
}

// The header inside the object is not secret either: the only key material
// in it is the data key WRAPPED to each recipient.
func TestTheFileHeaderHoldsTheDataKeyOnlyWrapped(t *testing.T) {
	if got, want := jsonKeys(t, reflect.TypeOf(FileHeader{})), []string{"backup_id", "chunk_size", "cipher", "format", "meta", "recipients"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("FileHeader fields %v, want %v", got, want)
	}
	if got, want := jsonKeys(t, reflect.TypeOf(WrappedKey{})), []string{"fingerprint", "name", "wrapped"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("WrappedKey fields %v, want %v", got, want)
	}
	if got, want := jsonKeys(t, reflect.TypeOf(Meta{})), []string{"host", "hz_version", "schema_version", "snapshot_at"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Meta fields %v, want %v", got, want)
	}
	k := testKey(t)
	h, _, err := ReadHeader(bytes.NewReader(encryptTo(t, []byte("x"), k)))
	if err != nil {
		t.Fatal(err)
	}
	blob, _ := configmgr.DecodeEnvelope(h.Recipients[0].Wrapped)
	eh, err := configmgr.ParseEnvelopeHeader(blob)
	if err != nil || eh.Kind != configmgr.KindBackupKey {
		t.Fatalf("wrapped key is not a kind 0x04 envelope: %v %v", eh, err)
	}
	raw, _ := json.Marshal(h)
	if bytes.Contains(raw, []byte("hzmprv_")) {
		t.Fatal("a private key in the header")
	}
}
