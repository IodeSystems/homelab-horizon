package server

import (
	"crypto/tls"
	"net/http/httptest"
	"testing"
)

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
