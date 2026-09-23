package server

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

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
		Hosts: make([]apitypes.HostView, 0, len(cfg.Hosts)+1),
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
	fillOccurrences(cfg, &selfView)
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
		fillOccurrences(cfg, &v)
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

// fillOccurrences attaches the OTHER list: the records that carry this host's
// address as a plain string.
//
// It is a separate field, filled by a separate call, and it is never appended
// to References. The two answer opposite questions — "what follows this host"
// and "what breaks when it moves" — and the only reason the screen is worth
// building is that it can now show both.
//
// A host with no address (only @self, before hz detects local_interface) is
// UNSCANNED rather than empty. There is nothing to search for, so reporting
// zero occurrences would be hz answering a question it never asked.
func fillOccurrences(cfg *config.Config, v *apitypes.HostView) {
	addr := strings.TrimSpace(v.IP)
	if addr == "" {
		v.OccurrencesKnown = false
		v.Occurrences = []apitypes.HostOccurrenceResp{}
		v.OccurrencesUnknownWhy = "hz does not know this host's address, so it cannot look for records " +
			"carrying it. This is an empty list because the search could not run, not because nothing " +
			"carries the address."
		return
	}
	v.OccurrencesKnown = true

	// The adoption plan, dry, so each occurrence carries what `hz host adopt`
	// would write here — or hz's reason for refusing. One computation behind
	// the screen and the CLI, so the screen cannot promise a rewrite the
	// command declines to do.
	plan, err := cfg.PlanAddressAdoption(addr)
	if err != nil {
		// Nothing declares the address, so nothing can be adopted into. The
		// occurrences are still real and still listed; only the "what would be
		// written" half is missing.
		occs := cfg.AddressOccurrences(addr)
		v.Occurrences = make([]apitypes.HostOccurrenceResp, 0, len(occs))
		for _, o := range occs {
			v.Occurrences = append(v.Occurrences, apitypes.HostOccurrenceResp{
				Kind: o.Kind, Owner: o.Owner, Field: o.Field, Value: o.Value, WhyNotAdoptable: err.Error(),
			})
		}
		return
	}

	v.Occurrences = make([]apitypes.HostOccurrenceResp, 0, len(plan.Adopt)+len(plan.Refused))
	for _, a := range plan.Adopt {
		v.Occurrences = append(v.Occurrences, adoptionToAPI(a))
	}
	for _, r := range plan.Refused {
		v.Occurrences = append(v.Occurrences, adoptionToAPI(r))
	}
	sortOccurrenceResps(v.Occurrences)
	if len(plan.Adopt) > 0 {
		// Named from the plan's own choice, not from this row: a nameless
		// declaration holding the gateway's address adopts to @self, and
		// echoing this row's (empty) name would print a command that fails.
		v.AdoptCommand = "hz host adopt " + strings.TrimPrefix(plan.Ref, config.HostRefSigil)
	}
}

// adoptionToAPI is the one place a config.HostAdoption becomes wire shape, so
// the CLI's list and the screen's list cannot describe the same record
// differently.
func adoptionToAPI(a config.HostAdoption) apitypes.HostOccurrenceResp {
	return apitypes.HostOccurrenceResp{
		Kind: a.Kind, Owner: a.Owner, Field: a.Field, Value: a.Value,
		Ref: a.Ref, WhyNotAdoptable: a.WhyNot,
	}
}

// sortOccurrenceResps restores the config's order (kind, owner, field) after
// the adoptable and refused halves are concatenated.
func sortOccurrenceResps(out []apitypes.HostOccurrenceResp) {
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		if out[i].Owner != out[j].Owner {
			return out[i].Owner < out[j].Owner
		}
		return out[i].Field < out[j].Field
	})
}
