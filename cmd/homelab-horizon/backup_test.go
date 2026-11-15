package main

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/iodesystems/homelab-horizon/configmgr"
	"github.com/iodesystems/homelab-horizon/internal/backup/offsite"
	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/db"
)

// captureS3 is a put-only bucket in memory that answers with the sha256 of
// what it received, as S3 does.
type captureS3 struct {
	keys    []string
	objects map[string][]byte
}

func (c *captureS3) PutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	b, err := io.ReadAll(in.Body)
	if err != nil {
		return nil, err
	}
	if c.objects == nil {
		c.objects = map[string][]byte{}
	}
	k := aws.ToString(in.Key)
	c.keys = append(c.keys, k)
	c.objects[k] = b
	sum := sha256.Sum256(b)
	return &s3.PutObjectOutput{ChecksumSHA256: aws.String(base64.StdEncoding.EncodeToString(sum[:]))}, nil
}

// pushBox is a box running hz: a config with paths in a temp dir, a token
// file, and the database held open with its recent rows still in the WAL.
type pushBox struct {
	configPath string
	cfg        *config.Config
	store      *db.DB
	key        *ecdh.PrivateKey
	dir        string
}

func newPushBox(t *testing.T, withRecipient bool) *pushBox {
	t.Helper()
	dir := t.TempDir()
	key, err := configmgr.NewMachineKey()
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		ListenAddr:   "127.0.0.1:0",
		WGInterface:  "wg0",
		VPNRange:     "10.100.0.0/24",
		UsersDB:      filepath.Join(dir, "data", "hz.db"),
		WGConfigPath: filepath.Join(dir, "wg0.conf"),
		InvitesFile:  filepath.Join(dir, "invites.txt"),
	}
	if withRecipient {
		cfg.RecoveryRecipients = []config.RecoveryRecipient{{Name: "offline-2026-10", PublicKey: configmgr.MarshalMachinePublicKey(key.PublicKey())}}
	}
	configPath := filepath.Join(dir, "config.json")
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath+".token", []byte("example-admin-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.WGConfigPath, []byte("[Interface]\nPrivateKey = example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := db.Open(cfg.UsersDB)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	store.SetMaxOpenConns(1)
	if _, err := store.Exec(`PRAGMA wal_autocheckpoint=0`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if _, err := store.CreateUser(context.Background(), fmt.Sprintf("op%d", i), "", db.RoleAdmin); err != nil {
			t.Fatal(err)
		}
	}
	return &pushBox{configPath: configPath, cfg: cfg, store: store, key: key, dir: dir}
}

func (b *pushBox) push(t *testing.T, api offsite.PutObjectAPI) (*offsite.PushResult, error) {
	t.Helper()
	return runBackupPush(context.Background(), b.configPath, pushFlags{dest: "s3://example-hz-backups/hz-office"},
		time.Date(2026, 10, 9, 4, 5, 6, 0, time.UTC),
		func(d *offsite.PushDeps) { d.API = api; d.TmpDir = t.TempDir() })
}

// push → (operator downloads) → decrypt with the offline key → restore → the
// next start's swap puts exactly the pushed snapshot in place.
func TestPushDecryptRestoreRoundTrip(t *testing.T) {
	box := newPushBox(t, true)
	bucket := &captureS3{}
	res, err := box.push(t, bucket)
	if err != nil {
		t.Fatal(err)
	}
	if len(bucket.keys) != 2 || !strings.HasSuffix(bucket.keys[0], ".hzbak") || !strings.HasSuffix(bucket.keys[1], ".json") {
		t.Fatalf("uploaded %v, want the object then the header", bucket.keys)
	}
	if !strings.HasPrefix(res.Object, "hz-office/") || res.Header.SchemaVersion == 0 {
		t.Fatalf("%+v", res)
	}
	obj := bucket.objects[res.Object]
	for _, marker := range []string{"SQLite format 3", "PK\x03\x04", "example-admin-token", "PrivateKey = example"} {
		if bytes.Contains(obj, []byte(marker)) || bytes.Contains(bucket.objects[res.Sidecar], []byte(marker)) {
			t.Fatalf("the bucket received plaintext: %q", marker)
		}
	}

	// A write after the push: the restore must bring back the snapshot, not
	// whatever the live file holds.
	if _, err := box.store.CreateUser(context.Background(), "after-push", "", db.RoleAdmin); err != nil {
		t.Fatal(err)
	}

	// The operator's download.
	dl := t.TempDir()
	hzbak := filepath.Join(dl, "20261009T040506Z.hzbak")
	if err := os.WriteFile(hzbak, obj, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dl, "20261009T040506Z.json"), bucket.objects[res.Sidecar], 0o600); err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(dl, "recovery-key.txt")
	if err := os.WriteFile(keyFile, []byte(configmgr.MarshalMachinePrivateKey(box.key)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// A wrong key writes nothing.
	other, _ := configmgr.NewMachineKey()
	wrongKey := filepath.Join(dl, "wrong.txt")
	_ = os.WriteFile(wrongKey, []byte(configmgr.MarshalMachinePrivateKey(other)), 0o600)
	if err := runBackupDecrypt(context.Background(), hzbak, wrongKey, "", nil, io.Discard); !errors.Is(err, offsite.ErrNotForThisKey) {
		t.Fatalf("wrong key: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dl, "20261009T040506Z.zip")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a wrong key left a file behind")
	}

	var out bytes.Buffer
	if err := runBackupDecrypt(context.Background(), hzbak, keyFile, "", nil, &out); err != nil {
		t.Fatalf("decrypt: %v\n%s", err, out.String())
	}
	zipPath := filepath.Join(dl, "20261009T040506Z.zip")
	if st, err := os.Stat(zipPath); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("decrypted zip: %v %v", st, err)
	}
	if !strings.Contains(out.String(), "matches: sha256") || !strings.Contains(out.String(), "verified") {
		t.Fatalf("decrypt output:\n%s", out.String())
	}

	// Restore onto a fresh config path; the database is staged beside the
	// path the restored config names.
	restoreTo := filepath.Join(t.TempDir(), "config.json")
	out.Reset()
	if err := runBackupRestore(context.Background(), restoreTo, zipPath, &out); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if b, err := os.ReadFile(restoreTo + ".token"); err != nil || string(b) != "example-admin-token\n" {
		t.Fatalf("token: %q %v", b, err)
	}
	dbPath := box.cfg.UsersDB
	if _, err := os.Stat(dbPath + db.PendingRestoreSuffix); err != nil {
		t.Fatalf("nothing staged: %v", err)
	}

	// The next start.
	_ = box.store.Close()
	if applied, err := db.ApplyPendingRestore(dbPath); err != nil || !applied {
		t.Fatalf("the start-time swap refused the staged restore: %v %v", applied, err)
	}
	store, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	var n int
	if err := store.QueryRow(`SELECT count(*) FROM users`).Scan(&n); err != nil || n != 5 {
		t.Fatalf("restored users: %d %v, want the 5 the push saw", n, err)
	}
}

func TestPushRefusesWithNoRecipients(t *testing.T) {
	box := newPushBox(t, false)
	bucket := &captureS3{}
	if _, err := box.push(t, bucket); !errors.Is(err, offsite.ErrNoRecipients) {
		t.Fatalf("got %v", err)
	}
	if len(bucket.keys) != 0 {
		t.Fatal("uploaded with nobody to encrypt to")
	}
}

func TestInstallPushTimerRendersTheUnits(t *testing.T) {
	dir := t.TempDir()
	root := newRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"--config", "/etc/homelab-horizon/config.json", "backup", "install-push-timer",
		"--dest", "s3://example-hz-backups/hz-instances/loadtest", "--aws-profile", "hz-backup", "--unit-dir", dir})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	svc, err := os.ReadFile(filepath.Join(dir, "hz-backup-push.service"))
	if err != nil {
		t.Fatal(err)
	}
	tmr, err := os.ReadFile(filepath.Join(dir, "hz-backup-push.timer"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Type=oneshot",
		"Environment=AWS_PROFILE=hz-backup\n",
		"Environment=AWS_CONFIG_FILE=/root/.aws/config\n",
		" --config /etc/homelab-horizon/config.json backup push --dest s3://example-hz-backups/hz-instances/loadtest\n",
	} {
		if !strings.Contains(string(svc), want) {
			t.Fatalf("service lacks %q:\n%s", want, svc)
		}
	}
	for _, want := range []string{"OnCalendar=daily\n", "Persistent=true\n", "RandomizedDelaySec=", "WantedBy=timers.target"} {
		if !strings.Contains(string(tmr), want) {
			t.Fatalf("timer lacks %q:\n%s", want, tmr)
		}
	}
	if strings.Contains(string(svc), "User=") {
		t.Fatal("the push service must run as root")
	}
	if !strings.Contains(out.String(), "not enabled") {
		t.Fatalf("output: %s", out.String())
	}
}

// At a terminal the key is ONE line after a prompt; reading to EOF looked
// hung until Ctrl-D (operator report). A pipe still reads to EOF.
func TestRecoveryKeyFromStdinReadsOneLineAtATerminal(t *testing.T) {
	r, w := io.Pipe()
	go func() { _, _ = w.Write([]byte("hzmprv_example\n")) }() // never closed: a terminal
	var prompt strings.Builder
	done := make(chan []byte, 1)
	go func() {
		got, err := readRecoveryKeyFromStdin(r, true, &prompt)
		if err != nil {
			t.Error(err)
		}
		done <- got
	}()
	select {
	case got := <-done:
		if strings.TrimSpace(string(got)) != "hzmprv_example" {
			t.Fatalf("got %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waited for EOF at a terminal: the read is still to EOF, not one line")
	}
	if !strings.Contains(prompt.String(), "Paste the recovery private key") {
		t.Fatalf("no prompt on a terminal: %q", prompt.String())
	}
	piped, err := readRecoveryKeyFromStdin(strings.NewReader("hzmprv_example"), false, io.Discard)
	if err != nil || string(piped) != "hzmprv_example" {
		t.Fatalf("pipe: %q, %v", piped, err)
	}
}
