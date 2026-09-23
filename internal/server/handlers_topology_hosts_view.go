package server

import (
	"encoding/json"
	"net/http"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
)

// GET /api/v1/topology/hosts/view — the host screen's one read.
//
// It is the same question `hz host show` answers, asked for every host at once
// and with the resolution carried alongside the authored value. See
// apitypes/hosts_view.go for why the screen needs both halves and why this is
// not /topology/hosts/show repeated.
//
// Nothing here is derived a second way: the host list is config.Hosts, the
// dependants are config.HostReferences, the kinds are config.HostRefKind*, and
// the resolution is config.ResolveHostRef — the same four the CLI and the
// renderers use. Admin-only, like the rest of /topology.
func (s *Server) handleAPITopologyHostsView(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(buildHostsView(s.cfg()))
}

// buildHostsView assembles the response from a config. Split out from the
// handler so it can be tested without a Server.
func buildHostsView(cfg *config.Config) apitypes.HostsViewResp {
	resp := apitypes.HostsViewResp{
		Hosts:            make([]apitypes.HostView, 0, len(cfg.Hosts)+1),
		LiteralsUnlisted: true,
	}

	// "@self" leads, as it does in `hz host list`. On a gateway it is the
	// reference that actually dominates — hz pointing at itself — and it is
	// the only spelling that stays correct on a peer, so it is the first row
	// rather than an appendix under the declarations.
	self := cfg.SelfHostDecl()
	selfView := apitypes.HostView{
		Name:        config.HostRefSelfName,
		IP:          self.IP,
		Self:        true,
		Ref:         config.SelfRef,
		Editable:    false,
		Addressable: self.IP != "",
		NotEditableWhy: "@self is not a declaration: it is whichever instance is running, so each " +
			"instance resolves it to its own address and a peer must not inherit this box's. " +
			"It moves by setting local_interface on the Settings page, and `hz host set self` is refused.",
		References: hostRefViews(cfg, config.HostRefSelfName),
	}
	if !selfView.Addressable {
		selfView.NotAddressableWhy = "hz has not detected this instance's own LAN address yet, so it " +
			"cannot say what @self means. Every record below is a reference hz can name and cannot " +
			"resolve — that is hz not knowing, not a record pointing at nothing. local_interface on " +
			"the Settings page is what fills it in."
	}
	resp.Hosts = append(resp.Hosts, selfView)

	for _, h := range cfg.Hosts {
		v := apitypes.HostView{
			Name:        h.Name,
			IP:          h.IP,
			Labels:      h.Labels,
			Addressable: h.IP != "",
			References:  hostRefViews(cfg, h.Name),
		}
		if h.Name == "" {
			// Legal, and a dead end worth saying out loud: a declaration with
			// no name carries an address and nothing can point at it, because
			// a reference is spelled by name. It will always show zero
			// dependants, which would otherwise read as "nothing needs this".
			v.Editable = false
			v.NotEditableWhy = "This declaration carries an address and no name. Nothing can reference " +
				"it — @ is spelled by name — and `hz host set` addresses a host by name too, so it " +
				"cannot be repointed from here. Give it a name on the Observability screen first."
		} else {
			v.Editable = true
			v.Ref = config.HostRefSigil + h.Name
		}
		resp.Hosts = append(resp.Hosts, v)
	}
	return resp
}

// hostRefViews is config.HostReferences with each authored value resolved
// beside it. A reference that cannot resolve carries hz's own sentence rather
// than an empty address: "hz cannot say where this points" and "this points
// nowhere" are different facts.
func hostRefViews(cfg *config.Config, name string) []apitypes.HostRefView {
	refs := cfg.HostReferences(name)
	out := make([]apitypes.HostRefView, 0, len(refs))
	for _, ref := range refs {
		v := apitypes.HostRefView{Kind: ref.Kind, Owner: ref.Owner, Field: ref.Field, Value: ref.Value}
		resolved, err := cfg.ResolveHostRef(ref.Value)
		if err != nil {
			v.ResolveError = err.Error()
		} else {
			v.Resolved = resolved
		}
		out = append(out, v)
	}
	return out
}
