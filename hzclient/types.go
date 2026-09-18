package hzclient

// The wire shapes, mirrored rather than imported.
//
// hz's own definitions live in internal/apitypes and internal/sitedeploy. This
// package is outside internal/ so an application can import it; importing
// internal/apitypes from here would still COMPILE — the internal rule is about
// where an import statement sits, not about transitive dependencies — but it
// would foreclose ever giving this package its own go.mod, because a nested
// module cannot import its parent's internal tree. That option is what keeps a
// consumer from inheriting hz's whole dependency graph (sqlite, WireGuard,
// ACME, seven DNS providers) in its module graph. configmgr/types.go made the
// same call for the same reason.
//
// The cost is two definitions of one JSON shape, and
// internal/server/hzclient_shapes_test.go is what bounds it: a field added on
// one side and not the other fails a test rather than silently reading empty in
// production. That failure mode is not hypothetical. It is the bans bug below.

// DeploySlotStatus is one slot's live state.
//
// State is what HAProxy reports through its stats socket — "up", "down",
// "drain", "maint" — or "unknown" when hz could not reach the socket at all.
// "unknown" is NOT a slot state: it means hz could not read one, and a caller
// that compares it against "up" is comparing a read failure against a state.
// The rolling phase machine treats it as unknown for exactly that reason.
type DeploySlotStatus struct {
	Slot    string `json:"slot"`
	Backend string `json:"backend"`
	State   string `json:"state"`
}

// DeployStatus is the whole answer to GET /api/deploy/status.
//
// The snake_case tags are hz's, not a style choice here: this family predates
// the camelCase convention the rest of the API uses, and a mirror that
// "corrected" them would decode to zero values. Mirror what is on the wire.
type DeployStatus struct {
	Service            string           `json:"service"`
	Domain             string           `json:"domain"`
	Domains            []string         `json:"domains,omitempty"`
	ActiveSlot         string           `json:"active_slot"`
	Balance            string           `json:"balance"`
	HealthCheck        string           `json:"health_check"`
	Current            DeploySlotStatus `json:"current"`
	Next               DeploySlotStatus `json:"next"`
	MaintenancePageMD5 string           `json:"maintenance_page_md5,omitempty"`
}

// Slot reports one slot by role, which is the only way a caller should address
// one. The letters a and b are hz's internal bookkeeping and they swap.
func (s *DeployStatus) Slot(slot SlotName) DeploySlotStatus {
	if slot == SlotCurrent {
		return s.Current
	}
	return s.Next
}

// DeployStateChangeResponse is the answer to a slot state change.
type DeployStateChangeResponse struct {
	Status string `json:"status"`
	Server string `json:"server"`
	State  string `json:"state"`
}

// DeploySwapResponse is the answer to a swap: the labels after it, not before.
type DeploySwapResponse struct {
	Status     string `json:"status"`
	ActiveSlot string `json:"active_slot"`
	Current    string `json:"current"`
	Next       string `json:"next"`
}

// MaintPageResponse answers both maint-page verbs.
//
// It has NO counterpart in internal/apitypes: the handler writes a
// map[string]string literal (internal/server/handlers_deploy.go, in
// handleDeployMaintPage), so there is no server type to pin this against
// structurally. MaintenancePageMD5 is absent from a clear, which is why it is
// omitempty and why an empty value here means "no page", not "unknown".
type MaintPageResponse struct {
	Status             string `json:"status"`
	MaintenancePageMD5 string `json:"maintenance_page_md5,omitempty"`
}

// BanRequest bans one address. Timeout is in SECONDS on the wire; BanAdd takes
// a time.Duration and converts, because seconds-as-int is the shape most likely
// to be handed a millisecond count by accident.
type BanRequest struct {
	IP      string `json:"ip"`
	Timeout int    `json:"timeout,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// UnbanRequest lifts one ban.
type UnbanRequest struct {
	IP string `json:"ip"`
}

// BanEntry is one active ban.
//
// CreatedAt and ExpiresAt are Unix SECONDS, and their tags are camelCase:
// createdAt and expiresAt. THIS IS THE BUG THIS WHOLE PACKAGE EXISTS TO END.
// bin/hz-client read created_at and expires_at, so every ban it ever printed
// said "created=- expires=never" — for as long as the file existed, past
// review, past a drift test that only compared the script to its own copy.
//
// A test that merely constructs this struct would miss it again, so the test
// that guards it decodes a real server-shaped payload and asserts the
// timestamps arrive NON-ZERO. See TestBanListDecodesServerTimestamps.
type BanEntry struct {
	IP        string `json:"ip"`
	Timeout   int    `json:"timeout,omitempty"`
	CreatedAt int64  `json:"createdAt"`
	ExpiresAt int64  `json:"expiresAt,omitempty"`
	Reason    string `json:"reason,omitempty"`
	Service   string `json:"service,omitempty"`
}

// Created reports the moment the ban was placed. Zero means hz sent no
// timestamp, which is a different fact from "banned at the epoch".
func (b BanEntry) Created() (sec int64, ok bool) { return b.CreatedAt, b.CreatedAt != 0 }

// Expires reports when the ban lapses. ok false means never: a permanent ban
// carries no expiry, and omitempty means the field is absent rather than zero.
func (b BanEntry) Expires() (sec int64, ok bool) { return b.ExpiresAt, b.ExpiresAt != 0 }

// BanListResponse wraps the list. Bans is nil when there are none.
type BanListResponse struct {
	Bans []BanEntry `json:"bans"`
}

// OKResponse is hz's acknowledgement of a ban or unban. The field is checked
// rather than ignored: a body that decodes with ok false is hz declining, and
// a 200 alone does not say which.
type OKResponse struct {
	OK bool `json:"ok"`
}

// SiteRelease is one retained release of a static site, newest first, with the
// live one marked. It mirrors internal/sitedeploy.Release.
type SiteRelease struct {
	ID      string `json:"id"`
	Current bool   `json:"current"`
}

// SiteRollbackResponse names the release now live.
//
// Like MaintPageResponse this has no server type to mirror — the handler writes
// map[string]string{"rolledBackTo": rel} in internal/server/handlers_site.go —
// so the drift test pins it by driving the real handler and decoding the real
// body through this type.
type SiteRollbackResponse struct {
	RolledBackTo string `json:"rolledBackTo"`
}
