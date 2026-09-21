package acme

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// bannedImports are packages the pure half must never reach for.
var bannedImports = map[string]string{
	"os":                    "reads or writes the filesystem, and sets process-global credentials",
	"os/exec":               "runs commands — including `aws` and `dig`",
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
	"crypto/x509":           "parses key material here; the pure half has no certificate to parse",
	"encoding/pem":          "decodes key material",
	"time":                  "reads the clock",
}

// bannedImportPrefixes catch whole trees. lego is the whole point: this is the
// package that talks to a certificate authority, so its pure half is defined
// by not being able to.
var bannedImportPrefixes = map[string]string{
	"github.com/go-acme/lego":                         "is an ACME client — it can reach a certificate authority",
	"github.com/aws/aws-sdk-go":                       "talks to AWS",
	"github.com/iodesystems/homelab-horizon/internal": "is another hz subsystem; the pure half needs none of them",
}

// keyMaterial are the shapes of "this code handles a private key" — the ACME
// ACCOUNT key here, which is the credential that proves to the CA that this is
// the same subscriber as last time.
//
// "keyauth" is on the list too: the DNS-01 key authorization is a secret
// derived from that key, and the pure half has no business computing one.
var keyMaterial = []string{
	"privatekey",
	"parseec",
	"parsepkcs",
	"marshalec",
	"marshalpkcs",
	"private key",
	"keyauth",
	"account.key",
}

// caReaching are the shapes of "this code can talk to a certificate
// authority". A renderer that can cannot be exercised without spending a
// rate-limited issuance — which is why the failure text an operator reads at
// 2am used to be unreachable in a test.
var caReaching = []string{
	"obtaincertificate",
	"cadirurl",
	"acme-v02",
	"letsencrypt.org",
	"ledirectory",
	"https://",
	"http://",
	"newacct",
	"setdns01provider",
	"newdnsprovider",
	"createchallengeprovider",
}

// secretFields are the fields of DNSProviderConfig that are credentials rather
// than identifiers. The pure half formats operator-facing text about a
// provider, so naming one of these in it is how a token ends up in a log.
var secretFields = []string{
	"AWSSecretAccessKey",
	"NamecomAPIToken",
	"CloudflareAPIToken",
}

func checkPurity(filename string, src any) ([]string, error) {
	f, err := parser.ParseFile(token.NewFileSet(), filename, src, 0)
	if err != nil {
		return nil, err
	}

	var bad []string
	for _, imp := range f.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		if why, isBanned := bannedImports[path]; isBanned {
			bad = append(bad, filename+" imports "+path+", which "+why+" — the pure half takes its inputs as arguments")
			continue
		}
		for prefix, why := range bannedImportPrefixes {
			if strings.HasPrefix(path, prefix) {
				bad = append(bad, filename+" imports "+path+", which "+why)
				break
			}
		}
	}

	// The type declaration itself names the secret fields, so only USES of
	// them are checked — a selector like cfg.AWSSecretAccessKey.
	ast.Inspect(f, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.SelectorExpr:
			for _, field := range secretFields {
				if v.Sel.Name == field {
					bad = append(bad, filename+" reads "+field+
						" — the pure half formats what an operator is shown, and that is a credential")
				}
			}
			return true
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
				bad = append(bad, filename+" contains the literal "+v.Value+" ("+hit+") — key material is apply-side")
			}
			if hit := hitIn(caReaching, lit); hit != "" {
				bad = append(bad, filename+" contains the literal "+v.Value+" ("+hit+") — reaching a certificate authority is apply-side")
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

// TestRenderStaysPure is the guard on the seam itself. This package's rule is
// stricter than the other seam packages': the pure half must be unable to reach
// a certificate authority, unable to touch the account key, and unable to print
// a provider credential.
func TestRenderStaysPure(t *testing.T) {
	bad, err := checkPurity("render.go", nil)
	if err != nil {
		t.Fatalf("parse render.go: %v", err)
	}
	for _, msg := range bad {
		t.Error(msg)
	}
}

// TestPurityGuardCatchesViolations is the positive control.
func TestPurityGuardCatchesViolations(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{"banned import", "package p\nimport \"os\"\nfunc f() { _ = os.Getenv(\"X\") }\n", "imports os"},
		{"exec import", "package p\nimport \"os/exec\"\nfunc f() { _ = exec.Command(\"dig\") }\n", "imports os/exec"},
		{"net/http", "package p\nimport \"net/http\"\nfunc f() { _, _ = http.Get(\"x\") }\n", "imports net/http"},
		{"reading the clock", "package p\nimport \"time\"\nfunc f() time.Time { return time.Now() }\n", "imports time"},
		{"lego", "package p\nimport \"github.com/go-acme/lego/v4/lego\"\nfunc f() {}\n", "go-acme/lego"},
		{"a lego provider", "package p\nimport \"github.com/go-acme/lego/v4/providers/dns/route53\"\nfunc f() {}\n", "go-acme/lego"},
		{"parsing the account key", "package p\nimport \"crypto/x509\"\nfunc f() {}\n", "imports crypto/x509"},
		{"minting a key", "package p\nimport \"crypto/ecdsa\"\nfunc f() {}\n", "imports crypto/ecdsa"},
		{"naming key material", "package p\nfunc loadPrivateKey() {}\n", "names loadPrivateKey"},
		{"the account key file", "package p\nfunc f() string { return \"account.key\" }\n", "account.key"},
		{"the challenge secret", "package p\nfunc f(keyAuth string) string { return keyAuth }\n", "keyauth"},
		{"asking a CA", "package p\nfunc ObtainCertificate() {}\n", "names ObtainCertificate"},
		{"a CA URL", "package p\nfunc f() string { return \"https://acme-v02.api.letsencrypt.org/directory\" }\n", "contains the literal"},
		{"building a challenge provider", "package p\nfunc CreateChallengeProvider() {}\n", "names CreateChallengeProvider"},
		{"printing a token", "package p\ntype c struct{ CloudflareAPIToken string }\nfunc f(x c) string { return x.CloudflareAPIToken }\n", "reads CloudflareAPIToken"},
		{"printing an AWS secret", "package p\ntype c struct{ AWSSecretAccessKey string }\nfunc f(x c) string { return x.AWSSecretAccessKey }\n", "reads AWSSecretAccessKey"},
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
			if joined := strings.Join(bad, "\n"); !strings.Contains(joined, tc.want) {
				t.Errorf("want a violation mentioning %q, got:\n%s", tc.want, joined)
			}
		})
	}

	// The negative control: a file that does only what render.go does must pass.
	clean := "package p\n\nimport (\n\t\"fmt\"\n\t\"strings\"\n)\n\n" +
		"func f(zone, out string) (string, []string) {\n" +
		"\tz := strings.TrimPrefix(zone, \"/hostedzone/\")\n" +
		"\treturn z, []string{fmt.Sprintf(\"  Zone name: %s\", strings.TrimSpace(out))}\n}\n"
	bad, err := checkPurity("clean.go", []byte(clean))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(bad) != 0 {
		t.Errorf("guard rejects a legitimately pure file:\n%s", strings.Join(bad, "\n"))
	}
}

// TestApplyStillHasTheSideEffects: a seam is only real if the privileged
// operations are somewhere.
func TestApplyStillHasTheSideEffects(t *testing.T) {
	src, err := os.ReadFile("apply.go")
	if err != nil {
		t.Fatalf("read apply.go: %v", err)
	}
	for _, want := range []string{
		"client.Certificate.Obtain(",    // the CA conversation
		"client.Registration.Register(", // the account
		"ecdsa.GenerateKey(",            // minting the account key
		`os.WriteFile(keyFile`,          // writing it
		`exec.Command("aws"`,            // the zone lookup
		`exec.Command("dig"`,            // the delegation check
		"os.Setenv(",                    // the provider credentials
	} {
		if !strings.Contains(string(src), want) {
			t.Errorf("apply.go no longer contains %q — if it moved, the seam moved with it", want)
		}
	}
}

// TestRenderNeedsNoCA exercises the pure half: every operator-facing verdict
// this package produces, computed from captured text, with no CA, no DNS and no
// credentials anywhere.
func TestRenderNeedsNoCA(t *testing.T) {
	if got := NormalizeZoneID("/hostedzone/ZEXAMPLE1"); got != "ZEXAMPLE1" {
		t.Errorf("NormalizeZoneID = %q", got)
	}
	if got := ParseZoneName("  example.test.\n"); got != "example.test" {
		t.Errorf("ParseZoneName = %q", got)
	}

	// The summary prints identifiers and never credentials. The config below
	// carries all three secrets; none may appear.
	cfg := &DNSProviderConfig{
		Type: DNSProviderRoute53, AWSProfile: "gw", AWSHostedZoneID: "ZEXAMPLE1", AWSRegion: "us-west-2",
		AWSAccessKeyID: "AKIAEXAMPLE", AWSSecretAccessKey: "s3cr3t-aws", NamecomAPIToken: "s3cr3t-nc",
		CloudflareAPIToken: "s3cr3t-cf",
	}
	head, tail := ProviderSummary(cfg)
	joined := strings.Join(append(append([]string{}, head...), tail...), "\n")
	for _, secret := range []string{"s3cr3t-aws", "s3cr3t-nc", "s3cr3t-cf"} {
		if strings.Contains(joined, secret) {
			t.Errorf("a credential reached the provider summary")
		}
	}
	if !strings.Contains(joined, "AWS Profile: gw") || !strings.Contains(joined, "AWS Hosted Zone ID: ZEXAMPLE1") ||
		!strings.Contains(joined, "AWS Region: us-west-2") {
		t.Errorf("summary lost an identifier:\n%s", joined)
	}
	if !ZoneVerifiable(cfg) {
		t.Error("a route53 config with a zone id is verifiable")
	}
	if ZoneVerifiable(&DNSProviderConfig{Type: DNSProviderRoute53}) {
		t.Error("no zone id means nothing to verify")
	}
	if ZoneVerifiable(nil) {
		t.Error("nil is not verifiable")
	}

	// The delegation verdicts, from captured dig output.
	lines, needNS := SOAReport("ns-1.awsdns-00.test. hostmaster.test. 1 7200 900 1209600 86400\n")
	if needNS || len(lines) != 1 || !strings.Contains(lines[0], "primary NS: ns-1.awsdns-00.test.") {
		t.Errorf("SOAReport(present) = %v, %v", lines, needNS)
	}
	if lines, needNS := SOAReport("   \n"); !needNS || len(lines) != 0 {
		t.Errorf("SOAReport(absent) = %v, %v", lines, needNS)
	}
	lines, err := NSReport("example.test", "ns-1.other.test.\nns-2.other.test.\n")
	if err == nil {
		t.Error("NS records but no SOA is a failed delegation")
	}
	if !strings.Contains(strings.Join(lines, "\n"), "ns-1.other.test., ns-2.other.test.") {
		t.Errorf("NSReport lost the record list:\n%v", lines)
	}
	lines, err = NSReport("example.test", "")
	if err == nil {
		t.Error("no NS records at all is a failed delegation")
	}
	if len(lines) != 4 {
		t.Errorf("NSReport(no records) = %v", lines)
	}

	// The failure hints.
	if got := ObtainFailureHints("boom, NXDOMAIN, boom"); len(got) != 1 || !strings.Contains(got[0], "zone ID is correct") {
		t.Errorf("ObtainFailureHints(NXDOMAIN) = %v", got)
	}
	if got := ObtainFailureHints("nothing recognisable"); got != nil {
		t.Errorf("an unrecognised error must not be guessed at: %v", got)
	}
	if got, want := ChallengeRecordName("example.test"), "_acme-challenge.example.test"; got != want {
		t.Errorf("ChallengeRecordName = %q", got)
	}
	if !reflect.DeepEqual(ProviderName(nil), "unknown") {
		t.Error("nil provider is unknown")
	}
}
