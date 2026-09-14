package server

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/portscan"
)

// GET /api/v1/ports/observed?host=IP&from=N&to=N
//
// What is actually listening, which is a different question from the derived
// map and the one the allocator should have been asking. hz's configuration
// covers what hz was told about; a host also runs things nobody registered,
// and suggesting one of their ports produces a service that cannot bind.
//
// Bounded and admin-only: this is a connect scan, and hz should not perform
// one on anybody's say-so or across arbitrary ranges.
func (s *Server) handleAPIPortsObserved(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "GET required")
		return
	}

	q := r.URL.Query()
	host := q.Get("host")
	if net.ParseIP(host) == nil {
		writeJSONError(w, http.StatusBadRequest, "host must be an IP address")
		return
	}
	from, _ := strconv.Atoi(q.Get("from"))
	to, _ := strconv.Atoi(q.Get("to"))
	if from <= 0 {
		from = 20000
	}
	if to <= 0 {
		to = from + 255
	}

	ports := portscan.Range(from, to)
	if len(ports) == 0 {
		writeJSONError(w, http.StatusBadRequest, "empty range: to must be >= from")
		return
	}

	// A scan is bounded in wall-clock as well as in size: this runs while
	// somebody waits on a CLI.
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()

	open := portscan.Observe(ctx, host, ports)

	// Which of those hz had no idea about — the part worth acting on.
	reserved := map[int]bool{}
	for _, e := range s.cfg().DeriveHostPortMap().Hosts[host] {
		if n, err := strconv.Atoi(e.Port); err == nil {
			reserved[n] = true
		}
	}
	var unreserved []int
	for _, p := range portscan.Sorted(open) {
		if !reserved[p] {
			unreserved = append(unreserved, p)
		}
	}

	writeJSON(w, apitypes.ObservedPortsResp{
		Host:       host,
		From:       ports[0],
		To:         ports[len(ports)-1],
		Open:       portscan.Sorted(open),
		Unreserved: unreserved,
		Scanned:    true,
	})
}
