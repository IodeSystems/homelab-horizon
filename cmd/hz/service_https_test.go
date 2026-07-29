package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
)

// stubHZ is a minimal hz instance: it serves the reads the CLI needs and
// records every request path, so a test can assert which mutations were (not)
// issued.
type stubHZ struct {
	mu       sync.Mutex
	requests []string
	services []apitypes.ServiceResp
	domains  []apitypes.DomainResp
}

func (h *stubHZ) record(path string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.requests = append(h.requests, path)
}

func (h *stubHZ) saw(path string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Contains(h.requests, path)
}

func (h *stubHZ) start(t *testing.T) *client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.record(r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/auth/login":
			_ = json.NewEncoder(w).Encode(apitypes.LoginResponse{OK: true})
		case "/api/v1/services":
			_ = json.NewEncoder(w).Encode(h.services)
		case "/api/v1/domains":
			_ = json.NewEncoder(w).Encode(apitypes.DomainsResponse{Domains: h.domains})
		default:
			_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
		}
	}))
	t.Cleanup(srv.Close)
	return newClient(srv.URL, "test-token")
}

func httpsTestStub() *stubHZ {
	return &stubHZ{
		services: []apitypes.ServiceResp{{
			Name:    "plain",
			Domains: []string{"plain.example.com"},
		}},
		domains: []apitypes.DomainResp{{
			Domain:      "plain.example.com",
			ZoneName:    "example.com",
			HasZone:     true,
			ZoneHasSSL:  true,
			ServiceName: "plain",
			HasService:  true,
			// No SSL coverage: --https would be a change to live state.
		}},
	}
}

// Without --confirm the edit must abort before anything is written — neither the
// service nor the zone's SubZones may be touched.
func TestServiceEditHTTPSWithoutConfirmMutatesNothing(t *testing.T) {
	stub := httpsTestStub()
	c := stub.start(t)

	err := serviceEdit(c, []string{"plain", "--https"})
	if err == nil {
		t.Fatal("expected an error demanding --confirm")
	}
	if !strings.Contains(err.Error(), "--confirm") {
		t.Errorf("error = %q, want it to ask for --confirm", err)
	}
	for _, mutation := range []string{"/api/v1/services/edit", "/api/v1/domains/ssl/add", "/api/v1/domains/ssl/remove"} {
		if stub.saw(mutation) {
			t.Errorf("aborted edit still called %s (requests: %v)", mutation, stub.requests)
		}
	}
}

// With --confirm the service edit lands first, then the HTTPS coverage.
func TestServiceEditHTTPSWithConfirmAppliesBoth(t *testing.T) {
	stub := httpsTestStub()
	c := stub.start(t)

	if err := serviceEdit(c, []string{"plain", "--https", "--confirm"}); err != nil {
		t.Fatalf("serviceEdit: %v", err)
	}
	if !stub.saw("/api/v1/services/edit") {
		t.Errorf("service was not edited (requests: %v)", stub.requests)
	}
	if !stub.saw("/api/v1/domains/ssl/add") {
		t.Errorf("HTTPS coverage was not added (requests: %v)", stub.requests)
	}
}

// No HTTPS flag at all: coverage is left completely alone.
func TestServiceEditWithoutHTTPSFlagsSkipsSSL(t *testing.T) {
	stub := httpsTestStub()
	c := stub.start(t)

	if err := serviceEdit(c, []string{"plain", "--public"}); err != nil {
		t.Fatalf("serviceEdit: %v", err)
	}
	if !stub.saw("/api/v1/services/edit") {
		t.Errorf("service was not edited (requests: %v)", stub.requests)
	}
	if stub.saw("/api/v1/domains") {
		t.Errorf("domain analysis fetched despite no HTTPS flag (requests: %v)", stub.requests)
	}
	if stub.saw("/api/v1/domains/ssl/add") {
		t.Errorf("SSL touched despite no HTTPS flag (requests: %v)", stub.requests)
	}
}
