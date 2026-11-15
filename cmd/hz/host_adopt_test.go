package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	hzconfig "github.com/iodesystems/homelab-horizon/internal/config"
)

// `hz host adopt` and the second half of `hz host show`, as an operator meets
// them.
//
// The stub is backed by a REAL config and calls the REAL planner
// (config.PlanAddressAdoption / AdoptAddress), for the reason declare_test.go
// gives: a stub that invents its own answers proves only that the renderer can
// render whatever it is handed. Here the record list, the refusals and the
// @self preference are the ones hz would actually give.
type adoptStub struct {
	cfg *hzconfig.Config
	// writes counts confirmed rewrites that reached the "server", so a test can
	// prove a dry run did not write rather than trusting what it printed.
	writes int
}

func newAdoptStub() *adoptStub {
	return &adoptStub{cfg: &hzconfig.Config{
		LocalInterface: "192.168.1.160",
		Hosts: []hzconfig.HostDecl{
			{Name: "gw", IP: "192.168.1.160"}, // the gateway declares itself
			{Name: "nas", IP: "192.168.1.51"},
		},
		Services: []hzconfig.Service{
			{
				Name:        "app",
				InternalDNS: &hzconfig.InternalDNS{IP: "192.168.1.160"},
				Proxy: &hzconfig.ProxyConfig{
					Backend: "192.168.1.160:8080",
					Deploy:  &hzconfig.DeployConfig{NextBackend: "192.168.1.160:8081", Token: "t"},
				},
			},
			{
				Name:  "wiki",
				Proxy: &hzconfig.ProxyConfig{Backend: "192.168.1.160:3000"},
			},
		},
	}}
}

func (s *adoptStub) start(t *testing.T) *client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		switch r.URL.Path {
		case "/api/v1/auth/login":
			_ = enc.Encode(apitypes.LoginResponse{OK: true})

		case "/api/v1/topology/hosts/adopt":
			var req apitypes.HostAdoptRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			addr := s.addressFor(req.Name)
			if addr == "" {
				w.WriteHeader(http.StatusNotFound)
				_ = enc.Encode(map[string]string{"error": "host not found: " + req.Name})
				return
			}
			plan, err := s.cfg.PlanAddressAdoption(addr)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				_ = enc.Encode(map[string]string{"error": err.Error()})
				return
			}
			out := apitypes.HostAdoptResp{Address: plan.Address, Ref: plan.Ref, RefWhy: plan.RefWhy}
			for _, a := range plan.Adopt {
				out.Adopt = append(out.Adopt, apitypes.HostOccurrenceResp{
					Kind: a.Kind, Owner: a.Owner, Field: a.Field, Value: a.Value, Ref: a.Ref,
				})
			}
			for _, rf := range plan.Refused {
				out.Refused = append(out.Refused, apitypes.HostOccurrenceResp{
					Kind: rf.Kind, Owner: rf.Owner, Field: rf.Field, Value: rf.Value, WhyNotAdoptable: rf.WhyNot,
				})
			}
			if req.Confirm {
				done, err := s.cfg.AdoptAddress(addr)
				if err != nil {
					w.WriteHeader(http.StatusBadRequest)
					_ = enc.Encode(map[string]string{"error": err.Error()})
					return
				}
				s.writes += done.Written
				out.Confirmed = true
				out.Written = done.Written
			}
			_ = enc.Encode(out)

		case "/api/v1/topology/hosts/show":
			name := r.URL.Query().Get("name")
			h := s.cfg.HostDeclByName(name)
			if name == hzconfig.HostRefSelfName {
				self := s.cfg.SelfHostDecl()
				h = &self
			}
			if h == nil {
				w.WriteHeader(http.StatusNotFound)
				_ = enc.Encode(map[string]string{"error": "host not found: " + name})
				return
			}
			out := apitypes.HostShowResp{
				Host:        apitypes.HostDecl{Name: h.Name, IP: h.IP},
				References:  []apitypes.HostReferenceResp{},
				Occurrences: []apitypes.HostOccurrenceResp{},
			}
			for _, ref := range s.cfg.HostReferences(name) {
				out.References = append(out.References, apitypes.HostReferenceResp{
					Kind: ref.Kind, Owner: ref.Owner, Field: ref.Field, Value: ref.Value,
				})
			}
			if h.IP != "" {
				out.OccurrencesKnown = true
				plan, err := s.cfg.PlanAddressAdoption(h.IP)
				if err == nil {
					for _, a := range plan.Adopt {
						out.Occurrences = append(out.Occurrences, apitypes.HostOccurrenceResp{
							Kind: a.Kind, Owner: a.Owner, Field: a.Field, Value: a.Value, Ref: a.Ref,
						})
					}
					for _, rf := range plan.Refused {
						out.Occurrences = append(out.Occurrences, apitypes.HostOccurrenceResp{
							Kind: rf.Kind, Owner: rf.Owner, Field: rf.Field, Value: rf.Value, WhyNotAdoptable: rf.WhyNot,
						})
					}
				}
			} else {
				out.OccurrencesUnknownWhy = "hz does not know this host's address, so it could not look."
			}
			// The real handler sorts the concatenated halves back into
			// (kind, owner, field); the CLI groups without re-sorting, so a
			// stub that skipped it would be testing a shape hz never sends.
			sort.SliceStable(out.Occurrences, func(i, j int) bool {
				a, b := out.Occurrences[i], out.Occurrences[j]
				if a.Kind != b.Kind {
					return a.Kind < b.Kind
				}
				if a.Owner != b.Owner {
					return a.Owner < b.Owner
				}
				return a.Field < b.Field
			})
			_ = enc.Encode(out)

		default:
			w.WriteHeader(http.StatusNotFound)
			_ = enc.Encode(map[string]string{"error": "unexpected path " + r.URL.Path})
		}
	}))
	t.Cleanup(srv.Close)
	return newClient(srv.URL, "test-token")
}

func (s *adoptStub) addressFor(name string) string {
	if name == hzconfig.HostRefSelfName {
		return s.cfg.LocalInterface
	}
	if h := s.cfg.HostDeclByName(name); h != nil {
		return h.IP
	}
	return ""
}

func captureHostStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	runErr := fn()
	_ = w.Close()
	os.Stdout = old
	return <-done, runErr
}

// THE DRY RUN IS THE PRODUCT. Without --confirm the command prints every
// record, says nothing was written, and nothing was.
func TestHostAdoptDryRunPrintsEveryRecordAndWritesNothing(t *testing.T) {
	s := newAdoptStub()
	c := s.start(t)

	out, err := captureHostStdout(t, func() error { return runHost(c, []string{"adopt", "self"}) })
	if err != nil {
		t.Fatalf("dry run: %v\n%s", err, out)
	}
	for _, want := range []string{
		"192.168.1.160",
		"@self",
		"proxy.backend",
		"proxy.deploy.next_backend",
		"internal_dns.ip",
		"192.168.1.160:8080",
		"@self:8080",
		"Dry run: nothing was written",
		"--confirm",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the dry run does not mention %q:\n%s", want, out)
		}
	}
	if s.writes != 0 {
		t.Errorf("the dry run wrote %d record(s)", s.writes)
	}
	if got := s.cfg.Services[0].Proxy.Backend; got != "192.168.1.160:8080" {
		t.Errorf("the config changed during a dry run: %q", got)
	}
}

// The @self preference is SHOWN, not applied quietly: the gateway declares
// itself as "gw", and an operator who did that deliberately is owed the reason
// hz picked the other spelling.
func TestHostAdoptExplainsWhySelfWon(t *testing.T) {
	s := newAdoptStub()
	c := s.start(t)

	out, err := captureHostStdout(t, func() error { return runHost(c, []string{"adopt", "gw"}) })
	if err != nil {
		t.Fatalf("dry run: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Adopts to: @self") {
		t.Errorf("adopting the gateway's own address did not choose @self:\n%s", out)
	}
	if !strings.Contains(out, `Why @self and not the other spelling`) || !strings.Contains(out, `"gw"`) {
		t.Errorf("the preference was applied without naming the host it beat:\n%s", out)
	}
}

// The refusals are printed with their reasons, never omitted. A record hz
// cannot adopt is still a record that breaks when the box moves.
func TestHostAdoptPrintsWhatItRefusesAndWhy(t *testing.T) {
	s := newAdoptStub()
	c := s.start(t)

	out, err := captureHostStdout(t, func() error { return runHost(c, []string{"adopt", "self"}) })
	if err != nil {
		t.Fatalf("dry run: %v\n%s", err, out)
	}
	for _, want := range []string{
		"will NOT rewrite",
		"local_interface",
		"cycle",
		"hosts[0].ip",
		"bottom out",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the refusal list does not mention %q:\n%s", want, out)
		}
	}
}

// --confirm writes, reports the count, and says what the move costs now.
func TestHostAdoptConfirmWrites(t *testing.T) {
	s := newAdoptStub()
	c := s.start(t)

	out, err := captureHostStdout(t, func() error { return runHost(c, []string{"adopt", "self", "--confirm"}) })
	if err != nil {
		t.Fatalf("confirmed adopt: %v\n%s", err, out)
	}
	if s.writes != 4 {
		t.Fatalf("wrote %d record(s), want 4 (2 backends, 1 standby slot, 1 dnsmasq answer)", s.writes)
	}
	if !strings.Contains(out, "Rewrote 4 record(s)") {
		t.Errorf("the confirmed run does not report what it wrote:\n%s", out)
	}
	if !strings.Contains(out, "local_interface") {
		t.Errorf("after adopting to @self, the move is local_interface — the output must say so:\n%s", out)
	}
	if strings.Contains(out, "Dry run") {
		t.Errorf("a confirmed run called itself a dry run:\n%s", out)
	}
	if got := s.cfg.Services[0].Proxy.Backend; got != "@self:8080" {
		t.Errorf("the record holds %q after --confirm", got)
	}
	// The declaration sites survived the write.
	if s.cfg.LocalInterface != "192.168.1.160" || s.cfg.Hosts[0].IP != "192.168.1.160" {
		t.Errorf("a declaration site was rewritten: local_interface=%q hosts[0].ip=%q", s.cfg.LocalInterface, s.cfg.Hosts[0].IP)
	}
}

// `hz host show` prints BOTH lists under separate headings. A gateway whose
// config is all literals has zero references and a page of occurrences, and
// printing one number for both is the bug this replaced.
func TestHostShowPrintsBothListsSeparately(t *testing.T) {
	s := newAdoptStub()
	c := s.start(t)

	out, err := captureHostStdout(t, func() error { return runHost(c, []string{"show", "self"}) })
	if err != nil {
		t.Fatalf("show: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Nothing references it.") {
		t.Errorf("nothing is written @self yet; the reference half must say so:\n%s", out)
	}
	if !strings.Contains(out, "CARRIES ITS ADDRESS AS A PLAIN STRING") {
		t.Errorf("the occurrence half is missing its own heading:\n%s", out)
	}
	if !strings.Contains(out, "do NOT follow") {
		t.Errorf("the output does not say occurrences behave differently from references:\n%s", out)
	}
	if !strings.Contains(out, "hz host adopt self") {
		t.Errorf("the output does not name the command that fixes it:\n%s", out)
	}
	// The two headings are not the same section.
	if strings.Index(out, "CARRIES ITS ADDRESS") < strings.Index(out, "Nothing references it.") {
		t.Error("the occurrence list must come after the reference list, as a separate section")
	}
}

// After adoption, `hz host show` flips: the records are references now, and
// nothing but the declaration sites carries the address.
func TestHostShowAfterAdoptionMovesRecordsToTheReferenceList(t *testing.T) {
	s := newAdoptStub()
	c := s.start(t)

	if _, err := captureHostStdout(t, func() error { return runHost(c, []string{"adopt", "self", "--confirm"}) }); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	out, err := captureHostStdout(t, func() error { return runHost(c, []string{"show", "self"}) })
	if err != nil {
		t.Fatalf("show: %v\n%s", err, out)
	}
	if !strings.Contains(out, "REFERENCED BY") {
		t.Errorf("the adopted records are not listed as references:\n%s", out)
	}
	if !strings.Contains(out, "4 record(s) resolve to THIS instance") {
		t.Errorf("the reference count did not move to 4:\n%s", out)
	}
	if !strings.Contains(out, "not adoptable") {
		t.Errorf("the two declaration sites should remain, listed as not adoptable:\n%s", out)
	}
}

// A host hz does not know is an error naming it, not an empty listing.
func TestHostAdoptUnknownHostIsAnError(t *testing.T) {
	s := newAdoptStub()
	c := s.start(t)
	if _, err := captureHostStdout(t, func() error { return runHost(c, []string{"adopt", "ghost"}) }); err == nil {
		t.Fatal("adopting an unknown host succeeded")
	}
}

// No name at all prints the usage rather than guessing a host.
func TestHostAdoptNeedsAName(t *testing.T) {
	s := newAdoptStub()
	c := s.start(t)
	_, err := captureHostStdout(t, func() error { return runHost(c, []string{"adopt"}) })
	if err == nil || !strings.Contains(err.Error(), "usage: hz host adopt") {
		t.Fatalf("want the usage, got %v", err)
	}
}
