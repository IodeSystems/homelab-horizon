package wireguard

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// bannedImports are packages the pure half must never reach for. The value is
// the sentence that gets printed, so it has to say why.
var bannedImports = map[string]string{
	"os":                           "reads or writes the filesystem",
	"os/exec":                      "runs commands — including `wg genkey`",
	"os/user":                      "reads the machine's account database",
	"net/http":                     "talks to the network",
	"io/ioutil":                    "reads or writes the filesystem",
	"path/filepath":                "resolves paths against a real filesystem",
	"syscall":                      "talks to the kernel",
	"golang.org/x/sys/unix":        "talks to the kernel",
	"math/rand":                    "is not deterministic",
	"math/rand/v2":                 "is not deterministic",
	"crypto/rand":                  "can mint a key",
	"crypto/ecdh":                  "can mint a key",
	"golang.org/x/crypto/nacl/box": "can mint a key",
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes": "can mint a key, and talks to the kernel",
}

// restrictedImports are packages whose NAME is I/O-shaped but which also carry
// pure value functions the renderer legitimately needs. Only the listed
// selectors are allowed; everything else in them is banned.
//
// This is stricter than an import list, not looser: internal/haproxy's guard
// bans `net` and `time` outright and can afford to, because its renderer needs
// neither. This one needs net.ParseCIDR to allocate an address and time.Unix to
// turn a dump's seconds into a Time, and an import-only check would have to
// either allow the whole package or reject the file.
var restrictedImports = map[string]map[string]bool{
	"net":  {"ParseCIDR": true, "IPv4": true, "IP": true, "IPNet": true, "ParseIP": true},
	"time": {"Time": true, "Unix": true, "Duration": true, "Second": true, "Minute": true, "Hour": true},
}

// keyMinting are the shapes of "this code can produce a key". A renderer that
// mints a key is not pure, not diffable and not testable by comparing bytes —
// which is the whole reason the seam exists here and not only in haproxy.
// Matched case-insensitively against identifiers and string literals, so a
// doc comment explaining the rule does not trip it.
//
// The rule is MINTING, not handling. GenerateClientConfig is handed the
// client's private key and has to interpolate it — a config with no
// PrivateKey line is not a config. What it must never do is produce that key
// itself, which is why the list names the generators (`wg genkey`,
// GeneratePrivateKey, curve25519) and not the word "privatekey".
var keyMinting = []string{
	"genkey",
	"generatekey",
	"generateprivatekey",
	"newprivatekey",
	"keypair",
	"curve25519",
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
			if hit := mintingHit(v.Name); hit != "" {
				bad = append(bad, filename+" names "+v.Name+" ("+hit+
					") — key generation is apply-side; render takes a public key as an argument")
			}
		case *ast.BasicLit:
			if v.Kind == token.STRING {
				if hit := mintingHit(strings.Trim(v.Value, "`\"")); hit != "" {
					bad = append(bad, filename+" contains the literal "+v.Value+" ("+hit+
						") — key generation is apply-side")
				}
			}
		}
		return true
	})

	return bad, nil
}

func mintingHit(s string) string {
	low := strings.ToLower(s)
	for _, k := range keyMinting {
		if strings.Contains(low, k) {
			return k
		}
	}
	return ""
}

// TestRenderStaysPure is the guard on the seam itself: render.go computes
// desired state and must not reach for the machine, so that half can run
// unprivileged (and off-box) once hz-agent owns the privileged half.
//
// An import list is a coarse check, but it is the one that catches the way this
// actually rots — someone adds an os.ReadFile to the renderer because the value
// was easier to read than to pass in. This package's guard goes one step
// further and checks selectors, because its renderer genuinely needs net and
// time for parsing and an all-or-nothing import rule would have to give up.
func TestRenderStaysPure(t *testing.T) {
	bad, err := checkPurity("render.go", nil)
	if err != nil {
		t.Fatalf("parse render.go: %v", err)
	}
	for _, msg := range bad {
		t.Error(msg)
	}
}

// TestRenderCannotMintAKey is the half of the guard specific to WireGuard.
//
// The other packages' renderers emit config for something that already has an
// identity. This one emits config that CONTAINS an identity, so the tempting
// mistake is for a render function to call `wg genkey` itself — at which point
// the renderer is non-deterministic, two calls with identical inputs produce
// different bytes, and nothing about it can be diffed or compared. Generation
// lives in apply.go; this pins that it stays there.
func TestRenderCannotMintAKey(t *testing.T) {
	src, err := os.ReadFile("render.go")
	if err != nil {
		t.Fatalf("read render.go: %v", err)
	}
	f, err := parser.ParseFile(token.NewFileSet(), "render.go", src, 0)
	if err != nil {
		t.Fatalf("parse render.go: %v", err)
	}
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if hit := mintingHit(fn.Name.Name); hit != "" {
			t.Errorf("render.go declares %s (%q) — key generation belongs in apply.go", fn.Name.Name, hit)
		}
	}

	// And the positive side of the same claim: the generator is where it should be.
	applySrc, err := os.ReadFile("apply.go")
	if err != nil {
		t.Fatalf("read apply.go: %v", err)
	}
	if !strings.Contains(string(applySrc), "func GenerateKeyPair(") {
		t.Error("GenerateKeyPair is not in apply.go — if it moved, the seam moved with it")
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
		{
			name: "banned import",
			src:  "package p\nimport \"os\"\nfunc f() { _, _ = os.ReadFile(\"x\") }\n",
			want: "imports os",
		},
		{
			name: "exec import",
			src:  "package p\nimport \"os/exec\"\nfunc f() { _ = exec.Command(\"wg\") }\n",
			want: "imports os/exec",
		},
		{
			name: "restricted selector on net",
			src:  "package p\nimport \"net\"\nfunc f() { _, _ = net.LookupHost(\"h\") }\n",
			want: "uses net.LookupHost",
		},
		{
			name: "restricted selector on time",
			src:  "package p\nimport \"time\"\nfunc f() { _ = time.Now() }\n",
			want: "uses time.Now",
		},
		{
			name: "aliased restricted import is still checked",
			src:  "package p\nimport n \"net\"\nfunc f() { _, _ = n.Dial(\"tcp\", \"h\") }\n",
			want: "uses n.Dial",
		},
		{
			name: "function that mints a key",
			src:  "package p\nfunc GenerateKeyPair() (string, string) { return \"\", \"\" }\n",
			want: "names GenerateKeyPair",
		},
		{
			name: "shelling out by string literal",
			src:  "package p\nfunc f() string { return \"genkey\" }\n",
			want: "contains the literal",
		},
		{
			name: "minting a private key",
			src:  "package p\nfunc GeneratePrivateKey() string { return \"\" }\n",
			want: "names GeneratePrivateKey",
		},
		{
			name: "rolling a key by hand",
			src:  "package p\nimport \"golang.org/x/crypto/curve25519\"\nfunc f(b []byte) []byte { return curve25519.Basepoint }\n",
			want: "names curve25519",
		},
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
	clean := "package p\n\nimport (\n\t\"fmt\"\n\t\"net\"\n\t\"time\"\n)\n\n" +
		"func f(cidr string, secs int64) (string, time.Time) {\n" +
		"\t_, n, _ := net.ParseCIDR(cidr)\n" +
		"\treturn fmt.Sprint(n), time.Unix(secs, 0)\n}\n"
	bad, err := checkPurity("clean.go", []byte(clean))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(bad) != 0 {
		t.Errorf("guard rejects a legitimately pure file:\n%s", strings.Join(bad, "\n"))
	}
}

// TestRenderNeedsNoMachine renders everything the pure half can render, naming
// paths and interfaces that do not exist. Nothing looks, because reading the
// machine is the apply half's job and its results arrive as arguments.
//
// The public key below is a fabricated fixture, not a key: the point is that
// render accepts one rather than producing one.
func TestRenderNeedsNoMachine(t *testing.T) {
	const fakePub = "FAKEFAKEnotarealkeyFAKEFAKEnotarealkeyFAKE="

	block := RenderPeerBlock("laptop", fakePub, "10.100.0.9/32")
	for _, want := range []string{"[Peer]", "# laptop", "PublicKey = " + fakePub, "AllowedIPs = 10.100.0.9/32"} {
		if !strings.Contains(block, want) {
			t.Errorf("want %q in peer block:\n%s", want, block)
		}
	}

	client := GenerateClientConfig("FAKEclientprivatefixture", "10.100.0.9", fakePub,
		"vpn.example.test:51820", "10.100.0.1", "0.0.0.0/0")
	for _, want := range []string{"Address = 10.100.0.9/24", "PublicKey = " + fakePub, "PersistentKeepalive = 25"} {
		if !strings.Contains(client, want) {
			t.Errorf("want %q in client config:\n%s", want, client)
		}
	}

	// The PostUp template names an interface this box does not have. The
	// renderer does not check, because detectDefaultInterface is apply-side.
	up := ExpectedPostUp("definitely-not-a-real-nic0")
	if !strings.Contains(up, "-o definitely-not-a-real-nic0 -j MASQUERADE") {
		t.Errorf("want the given interface interpolated verbatim:\n%s", up)
	}

	// Allocation from a set that was handed in, for a config no one has on disk.
	got, err := NextIP("10.77.0.0/24", []string{"10.77.0.1/24", "10.77.0.2/32", "10.77.0.3"})
	if err != nil {
		t.Fatalf("NextIP: %v", err)
	}
	if got != "10.77.0.4/32" {
		t.Errorf("NextIP = %s, want 10.77.0.4/32", got)
	}
}

// TestNextIPTakesTheUsedSetAsAnArgument pins the third fight's resolution: the
// same manager state produces the same answer whether it came off disk or out
// of a caller's head. GetNextIP is now a gather-and-delegate wrapper, and this
// is what says so.
func TestNextIPTakesTheUsedSetAsAnArgument(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wg0.conf")
	conf := "[Interface]\nPrivateKey = FAKEfixture\nAddress = 10.100.0.1/24\n\n" +
		"[Peer]\n# a\nPublicKey = FAKEpeera\nAllowedIPs = 10.100.0.2/32\n\n" +
		"[Peer]\n# b\nPublicKey = FAKEpeerb\nAllowedIPs = 10.100.0.3/32\n"
	if err := os.WriteFile(path, []byte(conf), 0600); err != nil {
		t.Fatal(err)
	}
	w := NewConfig(path, "wg0")
	if err := w.Load(); err != nil {
		t.Fatal(err)
	}

	fromDisk, err := w.GetNextIP("10.100.0.0/24")
	if err != nil {
		t.Fatal(err)
	}
	fromValues, err := NextIP("10.100.0.0/24", []string{"10.100.0.1/24", "10.100.0.2/32", "10.100.0.3/32"})
	if err != nil {
		t.Fatal(err)
	}
	if fromDisk != fromValues {
		t.Errorf("manager answered %s, pure allocator answered %s — the wrapper is doing more than gathering", fromDisk, fromValues)
	}
	if fromDisk != "10.100.0.4/32" {
		t.Errorf("got %s, want 10.100.0.4/32", fromDisk)
	}
}

// TestParseWGShowNeedsNoKernel covers the one extraction the old/new comparator
// could not reach: the `wg show` parser used to live inside GetInterfaceStatus,
// behind the exec that produces its input, so there was no way to run the
// pre-refactor version of it without a kernel. Pinned here instead.
func TestParseWGShowNeedsNoKernel(t *testing.T) {
	const out = `interface: wg0
  public key: FAKEserverkeyfixture
  private key: (hidden)
  listening port: 51820

peer: FAKEpeerAfixture
  endpoint: 192.0.2.10:34306
  allowed ips: 10.100.0.2/32
  latest handshake: 1 minute, 2 seconds ago
  transfer: 38.03 MiB received, 117.43 MiB sent

peer: FAKEpeerBfixture
  allowed ips: 10.100.0.3/32
`

	st := parseWGShow(out)
	if !st.Up {
		t.Error("text from wg show means the interface is up")
	}
	if st.PublicKey != "FAKEserverkeyfixture" {
		t.Errorf("public key = %q", st.PublicKey)
	}
	if st.Port != "51820" {
		t.Errorf("port = %q", st.Port)
	}
	if len(st.Peers) != 2 {
		t.Fatalf("parsed %d peers, want 2", len(st.Peers))
	}
	a := st.Peers["FAKEpeerAfixture"]
	if a.Endpoint != "192.0.2.10:34306" || a.AllowedIPs != "10.100.0.2/32" {
		t.Errorf("peer A = %+v", a)
	}
	if a.TransferRx != "38.03 MiB" || a.TransferTx != "117.43 MiB" {
		t.Errorf("transfer = rx %q tx %q", a.TransferRx, a.TransferTx)
	}
	b := st.Peers["FAKEpeerBfixture"]
	if b.AllowedIPs != "10.100.0.3/32" || b.Endpoint != "" {
		t.Errorf("peer B = %+v", b)
	}
}
