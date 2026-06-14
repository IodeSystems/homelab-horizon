package server

import (
	"crypto/tls"
	"net/http/httptest"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/config"
)

func TestIntegrationBaseURL(t *testing.T) {
	selfSvc := config.Service{Name: "admin", Domains: []string{"hz.example.com"}, Proxy: &config.ProxyConfig{Self: true}}

	tests := []struct {
		name string
		cfg  *config.Config
		xfp  string
		want string
	}{
		{
			name: "admin_url overrides everything (trailing slash trimmed)",
			cfg:  &config.Config{AdminURL: "https://hz.example.com/", SSLEnabled: true, Services: []config.Service{selfSvc}},
			want: "https://hz.example.com",
		},
		{
			name: "self service domain, https when SSL",
			cfg:  &config.Config{SSLEnabled: true, Services: []config.Service{selfSvc}},
			want: "https://hz.example.com",
		},
		{
			name: "no admin_url, no self service -> request host + forwarded scheme",
			cfg:  &config.Config{},
			xfp:  "https",
			want: "https://browsed.example.com",
		},
		{
			name: "plain http fallback to request host",
			cfg:  &config.Config{},
			want: "http://browsed.example.com",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Server{}
			s.config.Store(tt.cfg)
			r := httptest.NewRequest("GET", "/", nil)
			r.Host = "browsed.example.com"
			if tt.xfp != "" {
				r.Header.Set("X-Forwarded-Proto", tt.xfp)
			}
			if got := s.integrationBaseURL(r); got != tt.want {
				t.Errorf("integrationBaseURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRequestScheme(t *testing.T) {
	tests := []struct {
		name   string
		xfp    string
		tlsSet bool
		want   string
	}{
		{"forwarded https wins (TLS terminated at proxy)", "https", false, "https"},
		{"forwarded http", "http", false, "http"},
		{"forwarded https beats nil TLS", "https", false, "https"},
		{"direct TLS, no header", "", true, "https"},
		{"plain direct", "", false, "http"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			if tt.xfp != "" {
				r.Header.Set("X-Forwarded-Proto", tt.xfp)
			}
			if tt.tlsSet {
				r.TLS = &tls.ConnectionState{}
			}
			if got := requestScheme(r); got != tt.want {
				t.Errorf("requestScheme() = %q, want %q", got, tt.want)
			}
		})
	}
}
