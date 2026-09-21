package letsencrypt

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// bannedImports are packages the pure half must never reach for. The value is
// the sentence that gets printed, so it has to say why.
var bannedImports = map[string]string{
	"os":                    "reads or writes the filesystem",
	"os/exec":               "runs commands — including openssl",
	"os/user":               "reads the machine's account database",
	"net":                   "talks to the network",
	"net/http":              "talks to the network — and a CA is on the other end of it",
	"io/ioutil":             "reads or writes the filesystem",
	"path/filepath":         "resolves paths against a real filesystem",
	"syscall":               "talks to the kernel",
	"golang.org/x/sys/unix": "talks to the kernel",
	"math/rand":             "is not deterministic",
	"math/rand/v2":          "is not deterministic",
	"crypto/rand":           "can mint a key",
	"crypto/ecdsa":          "can mint a key",
	"crypto/rsa":            "can mint a key",
	"crypto/ed25519":        "can mint a key",
	"crypto/ecdh":           "can mint a key",
	"crypto/tls":            "opens a TLS connection",
}

// bannedImportPrefixes catch whole trees. The ACME client is the one that
// matters: importing it at all puts a certificate authority one call away from
// the half that is supposed to be runnable with nothing but a struct.
var bannedImportPrefixes = map[string]string{
	"github.com/go-acme/lego":                                 "is an ACME client — it can reach a certificate authority",
	"github.com/iodesystems/homelab-horizon/internal/acme":    "is this repo's ACME client — same objection",
	"github.com/iodesystems/homelab-horizon/internal/haproxy": "is the other side of the cert seam; the pure halves must not cross",
}

// restrictedImports are packages whose NAME is I/O-shaped but which also carry
// pure value functions the renderer legitimately needs. Only the listed
// selectors are allowed; everything else in them is banned.
//
// `time` is here rather than banned because the renewal decision is a
// comparison of two instants, and both of them arrive as arguments — what must
// stay out is READING the clock, which is the thing that makes a renewal
// boundary untestable. `crypto/x509` is here because parsing a CERTIFICATE is
// pure arithmetic over bytes somebody handed in, while parsing a KEY is not
// something this half may ever do.
var restrictedImports = map[string]map[string]bool{
	"time": {"Time": true, "Duration": true, "Hour": true, "Minute": true, "Second": true, "Unix": true},
	"crypto/x509": {
		"ParseCertificate": true, "Certificate": true,
	},
	"encoding/pem": {"Decode": true, "Block": true},
}

// keyMaterial are the shapes of "this code handles a private key". The pure
// half never does: it computes the PATH of privkey.pem and never its contents,
// which is why "privkey" is absent from this list and "private key" is on it.
//
// Matched case-insensitively against identifiers and string literals. A doc
// comment saying what the rule is does not trip it — comments are not parsed
// as either.
var keyMaterial = []string{
	"privatekey",
	"parseec",
	"parsepkcs",
	"marshalec",
	"marshalpkcs",
	"private key",
	"keyauth",
}

// caReaching are the shapes of "this code can talk to a certificate
// authority". This is the rule the other seam packages do not need: a renderer
// that can reach a CA cannot be exercised without spending a rate-limited
// issuance, which is exactly what makes certificate bugs ship.
var caReaching = []string{
	"obtaincertificate",
	"cadirurl",
	"acme-v02",
	"letsencrypt.org",
	"https://",
	"http://",
	"newacct",
}

// checkPurity is the guard itself, taken as a function over source so the test
// below can positive-control every rule against a file it wrote on purpose.
func checkPurity(filename string, src any) ([]string, error) {
	f, err := parser.ParseFile(token.NewFileSet(), filename, src, 0)
	if err != nil {
		return nil, err
	}

	var bad []string
	restrictedLocal := map[string]string{} // local name -> import path

	for _, imp := range f.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		if why, isBanned := bannedImports[path]; isBanned {
			bad = append(bad, filename+" imports "+path+", which "+why+" — the pure half takes its inputs as arguments")
			continue
		}
		banned := false
		for prefix, why := range bannedImportPrefixes {
			if strings.HasPrefix(path, prefix) {
				bad = append(bad, filename+" imports "+path+", which "+why)
				banned = true
				break
			}
		}
		if banned {
			continue
		}
		if _, restricted := restrictedImports[path]; restricted {
			local := path[strings.LastIndex(path, "/")+1:]
			if imp.Name != nil {
				local = imp.Name.Name
			}
			restrictedLocal[local] = path
		}
	}

	ast.Inspect(f, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.SelectorExpr:
			id, ok := v.X.(*ast.Ident)
			if !ok {
				return true
			}
			if path, restricted := restrictedLocal[id.Name]; restricted {
				if !restrictedImports[path][v.Sel.Name] {
					bad = append(bad, filename+" uses "+id.Name+"."+v.Sel.Name+
						" — only the pure value helpers of "+path+" are allowed here")
				}
			}
		case *ast.Ident:
			if hit := hitIn(keyMaterial, v.Name); hit != "" {
				bad = append(bad, filename+" names "+v.Name+" ("+hit+
					") — key material is apply-side; render never sees a key")
			}
			if hit := hitIn(caReaching, v.Name); hit != "" {
				bad = append(bad, filename+" names "+v.Name+" ("+hit+
					") — reaching a certificate authority is apply-side")
			}
		case *ast.BasicLit:
			if v.Kind != token.STRING {
				return true
			}
			lit := strings.Trim(v.Value, "`\"")
			if hit := hitIn(keyMaterial, lit); hit != "" {
				bad = append(bad, filename+" contains the literal "+v.Value+" ("+hit+
					") — key material is apply-side")
			}
			if hit := hitIn(caReaching, lit); hit != "" {
				bad = append(bad, filename+" contains the literal "+v.Value+" ("+hit+
					") — reaching a certificate authority is apply-side")
			}
		}
		return true
	})

	return bad, nil
}

func hitIn(list []string, s string) string {
	low := strings.ToLower(s)
	for _, k := range list {
		if strings.Contains(low, k) {
			return k
		}
	}
	return ""
}

// TestRenderStaysPure is the guard on the seam itself: render.go decides what
// should be true and must not reach for the machine, so that half can run
// unprivileged (and off-box) once hz-agent owns the privileged half.
//
// An import list is a coarse check, but it is the one that catches the way this
// actually rots — someone adds an os.ReadFile to the renderer because the value
// was easier to read than to pass in. This package's guard goes further and
// checks selectors and names, because its renderer genuinely needs x509 and
// time for parsing and comparing, while x509's key functions and time.Now must
// stay out.
func TestRenderStaysPure(t *testing.T) {
	bad, err := checkPurity("render.go", nil)
	if err != nil {
		t.Fatalf("parse render.go: %v", err)
	}
	for _, msg := range bad {
		t.Error(msg)
	}
}

// TestPurityGuardCatchesViolations is the positive control: a guard that cannot
// fail proves nothing, and an import-list check that silently stops matching
// looks exactly like a clean file.
func TestPurityGuardCatchesViolations(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{"banned import", "package p\nimport \"os\"\nfunc f() { _, _ = os.ReadFile(\"x\") }\n", "imports os"},
		{"exec import", "package p\nimport \"os/exec\"\nfunc f() { _ = exec.Command(\"openssl\") }\n", "imports os/exec"},
		{"filepath import", "package p\nimport \"path/filepath\"\nfunc f() string { return filepath.Join(\"a\", \"b\") }\n", "imports path/filepath"},
		{"the ACME client", "package p\nimport \"github.com/iodesystems/homelab-horizon/internal/acme\"\nfunc f() *acme.Client { return nil }\n", "internal/acme"},
		{"lego itself", "package p\nimport \"github.com/go-acme/lego/v4/lego\"\nfunc f() {}\n", "go-acme/lego"},
		{"reading the clock", "package p\nimport \"time\"\nfunc f() time.Time { return time.Now() }\n", "uses time.Now"},
		{"counting down from now", "package p\nimport \"time\"\nfunc f(t time.Time) time.Duration { return time.Until(t) }\n", "uses time.Until"},
		{"aliased restricted import is still checked", "package p\nimport tt \"time\"\nfunc f() tt.Time { return tt.Now() }\n", "uses tt.Now"},
		{"parsing a private key", "package p\nimport \"crypto/x509\"\nfunc f(b []byte) { _, _ = x509.ParseECPrivateKey(b) }\n", "uses x509.ParseECPrivateKey"},
		{"minting a key", "package p\nimport \"crypto/ecdsa\"\nfunc f() *ecdsa.PrivateKey { return nil }\n", "imports crypto/ecdsa"},
		{"naming key material", "package p\nfunc LoadPrivateKey() {}\n", "names LoadPrivateKey"},
		{"a PEM key block by literal", "package p\nfunc f() string { return \"EC PRIVATE KEY\" }\n", "contains the literal"},
		{"the challenge secret", "package p\nfunc f(keyAuth string) string { return keyAuth }\n", "keyauth"},
		{"asking a CA for a certificate", "package p\nfunc ObtainCertificate() {}\n", "names ObtainCertificate"},
		{"a CA URL by literal", "package p\nfunc f() string { return \"https://acme-v02.api.letsencrypt.org/directory\" }\n", "contains the literal"},
		{"crossing to haproxy", "package p\nimport \"github.com/iodesystems/homelab-horizon/internal/haproxy\"\nfunc f() haproxy.Cert { return haproxy.Cert{} }\n", "internal/haproxy"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "sample.go")
			if err := os.WriteFile(path, []byte(tc.src), 0644); err != nil {
				t.Fatal(err)
			}
			bad, err := checkPurity(path, []byte(tc.src))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if len(bad) == 0 {
				t.Fatalf("guard found nothing in:\n%s", tc.src)
			}
			joined := strings.Join(bad, "\n")
			if !strings.Contains(joined, tc.want) {
				t.Errorf("want a violation mentioning %q, got:\n%s", tc.want, joined)
			}
		})
	}

	// The negative control: a file that does only what render.go does must pass.
	clean := "package p\n\nimport (\n\t\"crypto/x509\"\n\t\"encoding/pem\"\n\t\"path\"\n\t\"strings\"\n\t\"time\"\n)\n\n" +
		"func f(b []byte, d string, now time.Time, days int) (string, bool) {\n" +
		"\tp := path.Join(\"/etc\", \"live\", strings.TrimPrefix(d, \"*.\"), \"privkey.pem\")\n" +
		"\tblk, _ := pem.Decode(b)\n" +
		"\tif blk == nil {\n\t\treturn p, true\n\t}\n" +
		"\tc, err := x509.ParseCertificate(blk.Bytes)\n" +
		"\tif err != nil {\n\t\treturn p, true\n\t}\n" +
		"\treturn p, c.NotAfter.Sub(now) < time.Duration(days)*24*time.Hour\n}\n"
	bad, err := checkPurity("clean.go", []byte(clean))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(bad) != 0 {
		t.Errorf("guard rejects a legitimately pure file:\n%s", strings.Join(bad, "\n"))
	}
}

// TestApplyStillHasTheSideEffects is the other half of the claim. A seam is
// only real if the privileged operations are somewhere; moving them out of
// apply.go should be as loud as smuggling them into render.go.
func TestApplyStillHasTheSideEffects(t *testing.T) {
	src, err := os.ReadFile("apply.go")
	if err != nil {
		t.Fatalf("read apply.go: %v", err)
	}
	for _, want := range []string{
		"m.acme.ObtainCertificate(",  // the CA conversation
		"os.WriteFile(paths.Privkey", // the key write
		"os.MkdirAll(paths.LiveDir",  // the 0700 directory
		"os.Remove(",                 // the prune
		`exec.Command("openssl"`,     // the inspection
	} {
		if !strings.Contains(string(src), want) {
			t.Errorf("apply.go no longer contains %q — if it moved, the seam moved with it", want)
		}
	}
}

// TestRenderNeedsNoMachine exercises the whole pure half against paths and
// certificates that do not exist. Nothing looks, because reading the machine is
// the apply half's job and its results arrive as arguments.
func TestRenderNeedsNoMachine(t *testing.T) {
	paths := CertPathsFor("/definitely/not/a/real/directory", "*.example.test")
	if paths.Fullchain != "/definitely/not/a/real/directory/live/example.test/fullchain.pem" {
		t.Errorf("Fullchain = %q", paths.Fullchain)
	}
	if paths.Privkey != "/definitely/not/a/real/directory/live/example.test/privkey.pem" {
		t.Errorf("Privkey = %q", paths.Privkey)
	}
	if got := HAProxyCertPathFor("/nowhere/certs", "*.example.test"); got != "/nowhere/certs/example.test.pem" {
		t.Errorf("HAProxyCertPathFor = %q", got)
	}

	d := DomainConfig{Domain: "*.example.test", ExtraSANs: []string{"example.test"}}
	if got := RequestedSANs(d); len(got) != 2 || got[0] != "*.example.test" || got[1] != "example.test" {
		t.Errorf("RequestedSANs = %v", got)
	}
	missing, extra := DiffSANs(d, []string{"example.test", "retired.example.test"})
	if len(missing) != 1 || missing[0] != "*.example.test" {
		t.Errorf("missing = %v", missing)
	}
	if len(extra) != 1 || extra[0] != "retired.example.test" {
		t.Errorf("extra = %v", extra)
	}

	orphans := OrphanedCertFiles([]DomainConfig{d}, []DirEntry{
		{Name: "example.test.pem"},
		{Name: "gone.example.test.pem"},
		{Name: "notes.txt"},
		{Name: "adir.pem", IsDir: true},
	})
	if len(orphans) != 1 || orphans[0] != "gone.example.test.pem" {
		t.Errorf("orphans = %v", orphans)
	}

	// The renewal boundary, tested from both sides AND exactly on it, without
	// waiting for it. The exact-boundary case is here because a `<` quietly
	// becoming `<=` is invisible to every fixture that is a whole day away
	// from the edge — found by sabotaging the comparator, which stayed green.
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if !NeedsRenewalAt(now.Add(29*24*time.Hour), now, 30) {
		t.Error("29 days out must renew within a 30 day window")
	}
	if NeedsRenewalAt(now.Add(30*24*time.Hour), now, 30) {
		t.Error("exactly 30 days out must NOT renew within a 30 day window — the window is strict")
	}
	if !NeedsRenewalAt(now.Add(30*24*time.Hour-time.Nanosecond), now, 30) {
		t.Error("a nanosecond inside the window must renew")
	}
	if NeedsRenewalAt(now.Add(31*24*time.Hour), now, 30) {
		t.Error("31 days out must not renew within a 30 day window")
	}
	if !NeedsRenewalAt(now.Add(-time.Hour), now, 30) {
		t.Error("an expired certificate must renew")
	}
	if _, ok := CertNotAfter([]byte("not a certificate")); ok {
		t.Error("garbage must not parse as a certificate")
	}
}
