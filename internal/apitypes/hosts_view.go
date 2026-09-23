package apitypes

// The host screen's read view: every declared host, plus "@self", with every
// record that resolves through each one and what that record resolves TO right
// now.
//
// WHY THIS IS NOT HostShowResp. `/topology/hosts/show` answers for ONE host and
// returns the authored value only — which is what `hz host show` prints, and
// enough for a terminal where the operator already knows the host list. A screen
// needs the whole list in one read (so a host with no dependants is drawn beside
// one with thirty, rather than being a second round-trip nobody makes), and it
// needs BOTH halves of the indirection on the page:
//
//   A record written "@nas:8080" renders as "@nas:8080 → 192.168.1.51:8080".
//
// Flattening it to the address loses the thing that makes the indirection
// visible — the operator cannot see that moving the box is one edit. Showing
// only "@nas" hides what it means today, which is the number they are about to
// compare against a router. Both, always, or the screen is lying by omission in
// one direction or the other.
//
// The grouping is NOT a new taxonomy: Kind carries the config.HostRefKind*
// strings verbatim, the same ones `hz host show` groups by and the same ones a
// removal refusal names. One vocabulary, three surfaces.

// HostRefView is one record that resolves through a host, with the authored
// value AND what it resolves to now.
//
// Resolved and ResolveError are the two halves of a state that must never
// collapse into a blank cell: a reference that cannot resolve is not a record
// pointing at nothing, it is hz unable to say where the record points.
type HostRefView struct {
	Kind  string `json:"kind"`  // config.HostRefKind*, verbatim
	Owner string `json:"owner"` // service name, DNS record name, exporter job; "" for config-level
	Field string `json:"field"` // e.g. "proxy.backend", "forwards[0].backend"
	Value string `json:"value"` // as authored: "@nas:8080"

	// Resolved is what Value means right now: "192.168.1.51:8080". Empty only
	// when resolution failed, in which case ResolveError carries hz's own
	// sentence about why (and how to fix it).
	Resolved string `json:"resolved,omitempty"`
	// ResolveError is hz's explanation when the reference cannot be resolved —
	// today that is "@self" on an instance whose local_interface has not been
	// detected. Empty when Resolved is set.
	ResolveError string `json:"resolveError,omitempty"`
}

// HostView is one host on the screen: a declaration, or the reserved "@self".
type HostView struct {
	// Name is the declared name, "" for a declaration that carries only an
	// address, or "self" for the reserved pseudo-host.
	Name   string            `json:"name"`
	IP     string            `json:"ip"`
	Labels map[string]string `json:"labels,omitempty"`

	// Self marks the reserved "@self" entry. It is NOT a declaration — nothing
	// declares it, nothing may, and `hz host set self` is refused — but it is
	// the reference that dominates a gateway, so it is a first-class row and
	// never a footnote.
	Self bool `json:"self"`

	// Ref is the spelling that points at this host ("@nas", "@self"), or ""
	// for a nameless declaration, which nothing can reference at all.
	Ref string `json:"ref,omitempty"`

	// Editable is whether THIS screen can repoint the host via
	// /topology/hosts/set. False for "@self" (per-instance, set by
	// local_interface) and for a nameless declaration (set is addressed by
	// name). NotEditableWhy is then the sentence saying so, never a disabled
	// control with no explanation.
	Editable       bool   `json:"editable"`
	NotEditableWhy string `json:"notEditableWhy,omitempty"`

	// Addressable is whether the host currently HAS an address to resolve to.
	// False only for "@self" before hz has detected local_interface — in which
	// case every reference below carries a ResolveError rather than an address.
	Addressable       bool   `json:"addressable"`
	NotAddressableWhy string `json:"notAddressableWhy,omitempty"`

	// References is every record that resolves through this host, sorted by
	// kind, then owner, then field — the server's order, which the client
	// groups by kind without re-sorting. Always non-nil: an empty list is the
	// answer "nothing points at this", which is different from not asking.
	References []HostRefView `json:"references"`
}

// HostsViewResp is the whole host screen in one read: "@self" first, then every
// declaration in config order.
//
// LiteralsUnlisted is the boundary this response cannot cross, stated rather
// than implied. HostReferences matches records written "@name"; a record that
// carries the same address as a LITERAL string resolves to the same box and
// hz cannot find it, because a literal says nothing about which machine it
// means. So an empty References list means "nothing REFERENCES this host", not
// "nothing breaks if it moves" — and a screen that renders those two the same
// way reintroduces exactly the bug host references were built to remove.
type HostsViewResp struct {
	Hosts []HostView `json:"hosts"`
	// LiteralsUnlisted is always true today. It is a field rather than a
	// hard-coded sentence in the client so that the day hz can enumerate
	// literal occurrences, the screen stops claiming it cannot.
	LiteralsUnlisted bool `json:"literalsUnlisted"`
}
