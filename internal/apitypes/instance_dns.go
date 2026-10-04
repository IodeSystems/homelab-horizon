package apitypes

// DNS for a nested hz (N4h, operator 2026-10-03: "if a parent OWNS a domain,
// then the child SYNC needs to push that sync to the parent").
//
// The CHILD asks; the PARENT publishes. A child serves names in a zone it does
// not own — the parent holds the zone and its DNS provider credential, and the
// credential never crosses (CLAUDE.md #3). So the child sends the records it
// wants, and the parent writes those inside the instance's DNS grant
// (HZInstance.DNSGrant), through the parent's own drift-safe publish:
//
//	POST /api/v1/hz-instances/<name>/dns   that instance's token only   InstanceDNSReq → InstanceDNSResp
//
// The direction is child → parent (CLAUDE.md #1): the parent never dials the
// child to learn what it serves.
//
// THE FULL SET, EVERY TIME. Records is everything the child wants published by
// the parent. A name the parent holds for this instance that is absent from
// the set is a removal request, and the parent removes it — only ever a record
// this instance owns. Idempotent: the same set twice changes nothing.

// HZInstanceDNSSuffix follows HZInstancesPath + "/<name>".
const HZInstanceDNSSuffix = "/dns"

// Instance DNS record status, as the parent answers it.
const (
	// InstanceDNSPublished: the parent's last publish of this (name, type)
	// wrote exactly these values.
	InstanceDNSPublished = "published"
	// InstanceDNSPending: accepted and stored on the parent; its DNS sync has
	// not written these values yet. Reason says why when the parent knows.
	InstanceDNSPending = "pending"
	// InstanceDNSRefused: not accepted. Reason names the rule and the record.
	InstanceDNSRefused = "refused"
)

// InstanceDNSRecordReq is one record set the child wants: every value at one
// (name, type). Type is A, AAAA, CNAME or TXT. TTL 0 is the parent's default.
type InstanceDNSRecordReq struct {
	Name   string   `json:"name"`
	Type   string   `json:"type"`
	Values []string `json:"values"`
	TTL    int      `json:"ttl,omitempty"`
}

// InstanceDNSReq is the child's full desired set. PublicIP is the address the
// child detected for itself (the A values it sends are built from it); the
// parent shows it on the instance page and writes nothing from it.
type InstanceDNSReq struct {
	Records  []InstanceDNSRecordReq `json:"records"`
	PublicIP string                 `json:"public_ip,omitempty"`
}

// InstanceDNSRecordStatus is the parent's answer for one requested record
// set. Converted is set once, on the request that turned a record the parent
// had declared by hand (with the identical value) into this instance's.
type InstanceDNSRecordStatus struct {
	Name      string   `json:"name"`
	Type      string   `json:"type"`
	Values    []string `json:"values"`
	Status    string   `json:"status"`
	Reason    string   `json:"reason,omitempty"`
	Converted bool     `json:"converted,omitempty"`
}

// InstanceDNSRemoved is a record set the parent removed because the set no
// longer lists it. It is retracted at the provider by the parent's sync.
type InstanceDNSRemoved struct {
	Name   string   `json:"name"`
	Type   string   `json:"type"`
	Values []string `json:"values"`
}

// InstanceDNSResp is the parent's answer: one status per requested record
// set, in request order, and what this request removed.
type InstanceDNSResp struct {
	Instance string                    `json:"instance"`
	Records  []InstanceDNSRecordStatus `json:"records"`
	Removed  []InstanceDNSRemoved      `json:"removed,omitempty"`
}

// InstanceOwnedRecordResp is one record set the PARENT holds for an instance,
// for the parent's instance page: what is stored, and whether it is published.
type InstanceOwnedRecordResp struct {
	Zone   string   `json:"zone"`
	Name   string   `json:"name"`
	Type   string   `json:"type"`
	Values []string `json:"values"`
	TTL    int      `json:"ttl"`
	Status string   `json:"status"` // published | pending
	Reason string   `json:"reason,omitempty"`
}

// InstanceDNSStatusResp is the parent's view of an instance's DNS: the grant
// (declared), the records it owns here (declared, with publish status), and
// the instance's last request (observed — memory, a restart forgets it).
type InstanceDNSStatusResp struct {
	Grant   []string                  `json:"grant"`
	Records []InstanceOwnedRecordResp `json:"records"`
	// LastRequestAt 0 is "no request since this hz started".
	LastRequestAt int64  `json:"lastRequestAt,omitempty"`
	LastPublicIP  string `json:"lastPublicIp,omitempty"`
	// LastRefused is every record set the last request asked for and this hz
	// refused, with the reason.
	LastRefused []InstanceDNSRecordStatus `json:"lastRefused,omitempty"`
}

// ParentDNSResp is what a CHILD shows of the DNS its parent publishes for it:
// present on the self row of /api/v1/instances when this hz has an upstream.
type ParentDNSResp struct {
	Parent   string `json:"parent"`
	Instance string `json:"instance"`
	// Enabled is false when this hz asks nothing of its parent (no
	// upstream.instance). Off says why.
	Enabled bool   `json:"enabled"`
	Off     string `json:"off,omitempty"`
	// Records is the parent's LAST answer, kept across failures and restarts.
	Records []InstanceDNSRecordStatus `json:"records"`
	Removed []InstanceDNSRemoved      `json:"removed,omitempty"`
	// LastAnswerAt is when that answer arrived (0: never); LastAttemptAt the
	// last try; LastError the last try's failure ("" when it succeeded).
	LastAnswerAt  int64  `json:"lastAnswerAt,omitempty"`
	LastAttemptAt int64  `json:"lastAttemptAt,omitempty"`
	LastError     string `json:"lastError,omitempty"`
	// Unasked is set when this hz did not ask this time and says why (its
	// public IP is unknown, so an absent A record would read as a removal).
	Unasked string `json:"unasked,omitempty"`
}
