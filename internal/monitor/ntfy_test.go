package monitor

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/config"
)

// ntfySeen records the Authorization header of every POST it takes.
type ntfySeen struct {
	mu    sync.Mutex
	auths []string
}

func (n *ntfySeen) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.auths = append(n.auths, r.Header.Get("Authorization"))
}

func (n *ntfySeen) got() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.auths...)
}

// hz's check notifier sends ntfy's access-token header exactly when a token
// is configured, and never logs it.
func TestSendNotificationBearer(t *testing.T) {
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	check := config.ServiceCheck{Name: "web", Type: "http", Target: "https://web.example.com"}
	const token = "tk_monitor_secret_77"

	for _, tc := range []struct {
		token, want string
	}{
		{token, "Bearer " + token},
		{"", ""},
	} {
		seen := &ntfySeen{}
		srv := httptest.NewServer(seen)
		cfg := &config.Config{NtfyURL: srv.URL + "/topic", NtfyToken: tc.token}
		New(cfg).sendNotification(check, errors.New("503"), StatusFailed)
		srv.Close()

		got := seen.got()
		if len(got) != 1 {
			t.Fatalf("token %q: ntfy got %d posts, want 1", tc.token, len(got))
		}
		if got[0] != tc.want {
			t.Fatalf("token %q: Authorization = %q, want %q", tc.token, got[0], tc.want)
		}
	}
	if strings.Contains(logs.String(), token) {
		t.Fatalf("the ntfy token reached the log:\n%s", logs.String())
	}
}
