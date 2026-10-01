package artifact

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func digest(b string) string {
	s := sha256.Sum256([]byte(b))
	return hex.EncodeToString(s[:])
}

func TestStorePutVerifiesAndLeavesNoTempOnRefusal(t *testing.T) {
	s := Store{Dir: filepath.Join(t.TempDir(), "artifacts")}
	body := "bundle-1.4.0"
	sha := digest(body)

	if _, err := s.Put(strings.NewReader(body), digest("other"), 1<<20); !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("mismatch: %v", err)
	}
	if _, err := s.Put(strings.NewReader(body), sha, 3); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("cap: %v", err)
	}
	entries, _ := os.ReadDir(s.Dir)
	if len(entries) != 0 {
		t.Fatalf("a refused upload left %d files behind: %v", len(entries), entries)
	}

	n, err := s.Put(strings.NewReader(body), sha, int64(len(body)))
	if err != nil || n != int64(len(body)) {
		t.Fatalf("put exactly at the cap: %d, %v", n, err)
	}
	st, err := os.Stat(s.Path(sha))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("stored file: %v, %v", st, err)
	}
	has, _ := s.Has(sha)
	f, size, err := s.Open(sha)
	if !has || err != nil || size != int64(len(body)) {
		t.Fatalf("has=%v open=%v size=%d", has, err, size)
	}
	_ = f.Close()
	list, _ := s.List()
	if len(list) != 1 || list[0] != sha {
		t.Fatalf("list = %v", list)
	}
	if err := s.Remove(sha); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove(sha); err != nil {
		t.Fatalf("removing a gone file: %v", err)
	}
	if _, err := s.Has("../etc/passwd"); err == nil {
		t.Fatal("a path traversal was accepted as a sha")
	}
}

const day = 86400

func sha(c byte) string { return strings.Repeat(string(c), 64) }

// The decision table. Every artifact here is 30 days old unless said, so age
// alone would delete it; each row is kept (or not) for exactly one reason.
func TestRetentionDecisionTable(t *testing.T) {
	prod := Rung{"redline", "prod"}
	staging := Rung{"redline", "staging"}
	unknown := Rung{"other", "prod"}
	now := int64(100 * day)
	old := now - 30*day

	in := Input{
		Now: now, MaxAgeSeconds: DefaultMaxAgeSeconds, KeepLastPromoted: DefaultKeepLastPromoted,
		Artifacts: []Record{
			{SHA: sha('a'), UploadedAt: old},                // promoted into prod long ago, on the supported prior line
			{SHA: sha('b'), UploadedAt: old},                // last 3 promoted
			{SHA: sha('c'), UploadedAt: old},                // last 3 promoted
			{SHA: sha('d'), UploadedAt: old},                // last 3 promoted and the newest apply
			{SHA: sha('e'), UploadedAt: old},                // promoted, off the supported lines, 4th back: DELETE
			{SHA: sha('f'), UploadedAt: old},                // staging's newest report
			{SHA: sha('1'), UploadedAt: old},                // pinned by nothing: DELETE
			{SHA: sha('2'), UploadedAt: now - 6*day},        // pinned by nothing, young: kept by age
			{SHA: sha('3'), UploadedAt: old, Deleted: true}, // already gone: neither
			{SHA: sha('4'), UploadedAt: old},                // promoted into a rung whose lines are unknown
		},
		Promotions: []Pin{
			{Rung: prod, SHA: sha('a'), Line: "1.8.0", ID: 1},
			{Rung: prod, SHA: sha('e'), Line: "1.7.0", ID: 2},
			{Rung: prod, SHA: sha('b'), Line: "1.9.0", ID: 3},
			{Rung: prod, SHA: sha('c'), Line: "1.9.0", ID: 4},
			{Rung: prod, SHA: sha('d'), Line: "1.9.0", ID: 5},
			{Rung: unknown, SHA: sha('4'), Line: "", ID: 6},
		},
		Applies:       []Pin{{Rung: prod, SHA: sha('d'), Line: "1.9.0", ID: 1}},
		NewestReports: []Pin{{Rung: staging, SHA: sha('f'), Line: "2.0.0", ID: 9}},
		Supported:     map[Rung][]string{prod: {"1.9.0", "1.8.0"}},
		LinesUnknown:  map[Rung]string{unknown: "a report hz cannot read a line from"},
	}
	dels, kept := Decide(in)
	var got []string
	for _, d := range dels {
		got = append(got, d.SHA[:1])
		if !strings.Contains(d.Why, "not kept") || !strings.Contains(d.Why, "30 days") {
			t.Errorf("deletion %s says %q", d.SHA[:1], d.Why)
		}
	}
	if strings.Join(got, ",") != "1,e" {
		t.Fatalf("deleted %v, want [1 e]", got)
	}
	for c, want := range map[byte]string{
		'a': "on its supported line 1.8.0",
		'b': "among the last 3",
		'd': "the newest apply into redline/prod",
		'f': "redline/staging's newest report",
		'4': "cannot derive other/prod's supported lines",
	} {
		if !strings.Contains(strings.Join(kept[sha(c)], "; "), want) {
			t.Errorf("%c kept for %v, want %q", c, kept[sha(c)], want)
		}
	}
	if _, ok := kept[sha('2')]; ok {
		t.Error("a young unpinned artifact was KEPT for a reason; it is spared by age only")
	}

	// The newest apply is kept even off every supported line and outside the
	// last 3: it is what the rung's children pull now.
	in2 := Input{
		Now: now, MaxAgeSeconds: DefaultMaxAgeSeconds, KeepLastPromoted: 1,
		Artifacts:  []Record{{SHA: sha('x'), UploadedAt: old}, {SHA: sha('y'), UploadedAt: old}},
		Promotions: []Pin{{Rung: prod, SHA: sha('x'), Line: "0.1.0", ID: 1}, {Rung: prod, SHA: sha('y'), Line: "0.2.0", ID: 2}},
		Applies:    []Pin{{Rung: prod, SHA: sha('x'), Line: "0.1.0", ID: 1}},
	}
	dels, _ = Decide(in2)
	if len(dels) != 0 {
		t.Fatalf("deleted %+v; x is the newest apply and y the last promoted", dels)
	}
}
