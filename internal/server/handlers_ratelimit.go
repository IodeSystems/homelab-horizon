package server

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/haproxy"
)

// The edge rate limit (EDGE-4).
//
// Its own endpoint rather than a corner of /settings: turning it on changes how
// the gateway answers strangers, and the threshold is the kind of number an
// operator revisits after an incident.

type rateLimitResponse struct {
	Enabled       bool `json:"enabled"`
	WindowSeconds int  `json:"windowSeconds"`
	Requests      int  `json:"requests"`
	ExemptLocal   bool `json:"exemptLocal"`
	// Active is false when nothing would be enforced despite being enabled —
	// no global threshold and no service overriding it. Worth reporting
	// separately, because "on but inert" is otherwise invisible.
	Active bool `json:"active"`
	// Overrides lists services that set their own threshold, so the page can
	// show what the default does and does not govern.
	Overrides map[string]int `json:"overrides,omitempty"`
}

func (s *Server) handleAPIRateLimit(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	switch r.Method {
	case http.MethodGet:
		cfg := s.cfg()
		resp := rateLimitResponse{
			WindowSeconds: cfg.RateLimit.EffectiveWindowSeconds(),
			Requests:      cfg.RateLimit.EffectiveRequests(),
			ExemptLocal:   cfg.RateLimit.ExemptsLocal(),
			Active:        cfg.RateLimitActive(),
			Overrides:     map[string]int{},
		}
		if cfg.RateLimit != nil {
			resp.Enabled = cfg.RateLimit.Enabled
			resp.Requests = cfg.RateLimit.Requests
		}
		for _, svc := range cfg.Services {
			if svc.Proxy != nil && svc.Proxy.RateLimitRequests != 0 {
				resp.Overrides[svc.Name] = svc.Proxy.RateLimitRequests
			}
		}
		_ = json.NewEncoder(w).Encode(resp)

	case http.MethodPost:
		var body struct {
			Enabled       bool  `json:"enabled"`
			WindowSeconds int   `json:"windowSeconds"`
			Requests      int   `json:"requests"`
			ExemptLocal   *bool `json:"exemptLocal"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSONError(w, http.StatusBadRequest, "Invalid request body")
			return
		}

		// Bounds, not free numbers. A one-second window measures jitter; a
		// threshold of 1 denies a page load, since a browser fetches a dozen
		// assets for one visit.
		switch {
		case body.WindowSeconds < 0 || body.WindowSeconds > 3600:
			writeJSONError(w, http.StatusBadRequest, "windowSeconds must be between 0 (default) and 3600")
			return
		case body.WindowSeconds > 0 && body.WindowSeconds < 2:
			writeJSONError(w, http.StatusBadRequest, "a window under 2 seconds measures jitter rather than volume")
			return
		case body.Requests < 0:
			writeJSONError(w, http.StatusBadRequest, "requests cannot be negative here; set -1 on a service to exempt it")
			return
		case body.Enabled && body.Requests > 0 && body.Requests < 5:
			writeJSONError(w, http.StatusBadRequest,
				"a threshold under 5 requests per window will deny ordinary page loads, which fetch many assets at once")
			return
		}

		if err := s.updateConfig(func(c *config.Config) {
			if c.RateLimit == nil {
				c.RateLimit = &config.RateLimit{}
			}
			c.RateLimit.Enabled = body.Enabled
			c.RateLimit.WindowSeconds = body.WindowSeconds
			c.RateLimit.Requests = body.Requests
			c.RateLimit.ExemptLocal = body.ExemptLocal
		}); err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to save config: "+err.Error())
			return
		}

		// The rules live in haproxy.cfg itself, not in a file it reads, so the
		// config has to be regenerated and reloaded rather than just rewritten.
		if err := s.applyHAProxyConfig(); err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}

		slog.Info("edge rate limit updated", "enabled", body.Enabled,
			"window_s", body.WindowSeconds, "requests", body.Requests, "by", s.adminActor(r))
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})

	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

// applyHAProxyConfig regenerates and reloads haproxy.cfg.
//
// Shares the shape of applyMFAJailConfig, but returns its error instead of only
// logging: this one is called straight from a handler, and an operator who just
// set a threshold needs to know whether the edge actually took it.
func (s *Server) applyHAProxyConfig() error {
	cfg := s.cfg()
	if !cfg.HAProxyEnabled {
		// Stored and applied whenever HAProxy is turned on, the same way a
		// local DNS record waits for dnsmasq.
		return nil
	}

	backends := cfg.DeriveHAProxyBackends()
	s.haproxy.SetBackends(backends)
	s.haproxy.SetMFAJail(mfaJailFor(cfg, backends))
	s.haproxy.SetRateLimit(rateLimitFor(cfg))

	var sslConfig *haproxy.SSLConfig
	if cfg.SSLEnabled {
		sslConfig = &haproxy.SSLConfig{Enabled: true, CertDir: cfg.SSLHAProxyCertDir}
	}
	if err := s.haproxy.WriteConfig(cfg.HAProxyHTTPPort, cfg.HAProxyHTTPSPort, sslConfig); err != nil {
		return fmt.Errorf("write haproxy config: %w", err)
	}
	if err := s.haproxy.Reload(); err != nil {
		return fmt.Errorf("haproxy reload: %w", err)
	}
	return nil
}
