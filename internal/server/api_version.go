package server

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/iodesystems/homelab-horizon/hzapi"
)

// apiVersionMiddleware settles "can these talk" before a handler runs.
//
// It exists because the answer was previously nobody's job. `hz-client bans`
// had never printed a timestamp — the server marshalled createdAt/expiresAt and
// the script read created_at/expires_at — and nothing noticed, for as long as
// that code has existed. A contract drifted inside one repository, past review,
// past a drift test that only compared the script to its own copy.
//
// Pinned clients make that worse rather than better: a linked library is
// whatever a consumer compiled months ago. So a version travels on every
// request, and a mismatch is refused HERE, once, rather than surfacing as a
// missing field somewhere deep in a handler.
//
// Applied to /api/ only. The admin UI is served from the same binary as the
// server it talks to, so it cannot skew; static assets and the downloadable
// client script have no contract to check.
func (s *Server) apiVersionMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}

		// Advertised on every response including refusals, so a client that has
		// just been turned away can report what turned it away.
		hzapi.Advertise(w.Header())

		version, declared := hzapi.FromRequest(r)
		if !declared {
			// No header: a client built before this existed. hz-client is
			// exactly that, and refusing it would have broken every consumer
			// the moment this shipped.
			//
			// Logged rather than silent, because the log is the evidence for
			// eventually flipping hzapi.UnversionedOK — that should be a
			// decision someone makes holding proof that nothing unversioned
			// remains, not a default that drifts into place.
			if !hzapi.UnversionedOK {
				writeJSONError(w, http.StatusBadRequest,
					"this endpoint requires the "+hzapi.HeaderVersion+" header")
				return
			}
			// RemoteAddr rather than getClientIP: this middleware runs before
			// EVERY api request, and getClientIP reads the config, so a
			// half-built server would panic here and take the whole API with
			// it. A debug aid must not be able to do that. The user agent is
			// the identifying signal anyway — it is what says "hz-client".
			slog.Debug("unversioned API request",
				"path", r.URL.Path, "agent", r.UserAgent(), "from", r.RemoteAddr)
			next.ServeHTTP(w, r)
			return
		}

		if err := hzapi.Check(version); err != nil {
			// 400, not 426 Upgrade Required: 426 is specific to switching
			// protocols on the same connection, and a proxy that understands it
			// may act on it. This is an application-level refusal and the body
			// is what carries the meaning.
			slog.Warn("refused an API request on version",
				"path", r.URL.Path, "client", version,
				"serverMin", hzapi.MinVersion, "serverCurrent", hzapi.Version)
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		next.ServeHTTP(w, r)
	})
}
