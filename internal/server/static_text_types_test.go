package server

import (
	"mime"
	"testing"
)

// An install script and a public key serve as text on every host. The
// fallback is logged as the control: on the office gateway it answered
// application/vnd.ms-publisher for .pub (a static site, 2026-11-10).
func TestInstallScriptAndPublicKeyServeAsText(t *testing.T) {
	for _, name := range []string{"install.sh", "minisign.pub", "id_ed25519.PUB"} {
		if got := contentType(name); got != "text/plain; charset=utf-8" {
			t.Errorf("%s: Content-Type %q, want text/plain", name, got)
		}
	}
	t.Logf("host mime.types fallback: .sh=%q .pub=%q", mime.TypeByExtension(".sh"), mime.TypeByExtension(".pub"))
}
