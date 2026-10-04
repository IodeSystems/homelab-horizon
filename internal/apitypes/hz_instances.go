package apitypes

// Hz instances: the nested hz this one serves rungs to (config.HZInstance).
// NOT machines — an hz instance never appears in /api/v1/machines.
//
//	GET  /api/v1/hz-instances          admin   []HZInstanceResp
//	POST /api/v1/hz-instances/add      admin   HZInstanceAddReq → HZInstanceResp
//	POST /api/v1/hz-instances/set      admin   HZInstanceSetReq → HZInstanceResp
//	POST /api/v1/hz-instances/rm       admin   HZInstanceRmReq  → RemovalResp (dry run without confirm)
//	POST /api/v1/hz-instances/token    admin   InstanceTokenReq → InstanceTokenResp (shown once)
//
// POST /api/v1/machines/hz-token is the same mint, kept one release for the
// `hz machine hz-token` alias.

// HZInstancesPath is the list.
const HZInstancesPath = "/api/v1/hz-instances"

// HZInstanceResp is one nested hz. HasToken says an instance token is minted;
// the token and its digest are never sent.
type HZInstanceResp struct {
	Name      string `json:"name"`
	Project   string `json:"project"`
	URL       string `json:"url"`
	VPNClient string `json:"vpnClient,omitempty"`
	Note      string `json:"note,omitempty"`
	HasToken  bool   `json:"hasToken"`
	// Domains are the hostnames the instance terminates TLS for; this hz
	// issues one certificate covering exactly these (InstanceCertResp).
	Domains []string `json:"domains,omitempty"`
	// DNSGrant is where this hz publishes DNS the instance asks for: exact
	// names and "*.suffix" subtrees in zones this hz owns with a provider.
	// Domains lie inside it.
	DNSGrant []string `json:"dnsGrant,omitempty"`
}

// HZInstanceAddReq declares a nested hz. URL must be http(s) with a host;
// VPNClient, when set, must name an existing `upstream` VPN client.
type HZInstanceAddReq struct {
	Name      string   `json:"name"`
	Project   string   `json:"project,omitempty"`
	URL       string   `json:"url"`
	VPNClient string   `json:"vpnClient,omitempty"`
	Note      string   `json:"note,omitempty"`
	Domains   []string `json:"domains,omitempty"`
	DNSGrant  []string `json:"dnsGrant,omitempty"`
}

// HZInstanceSetReq edits an instance in place. nil leaves a field alone; a
// non-nil "" clears it (Project: global; VPNClient: unlink). The token is not
// editable — mint a new one.
type HZInstanceSetReq struct {
	Name      string  `json:"name"`
	Project   *string `json:"project,omitempty"`
	URL       *string `json:"url,omitempty"`
	VPNClient *string `json:"vpnClient,omitempty"`
	Note      *string `json:"note,omitempty"`
	// Domains: nil leaves them, [] clears them.
	Domains *[]string `json:"domains,omitempty"`
	// DNSGrant: nil leaves it, [] revokes it. Narrowing it removes every
	// record the instance owns outside the new grant (retracted at the
	// provider by the next sync).
	DNSGrant *[]string `json:"dnsGrant,omitempty"`
}

// HZInstanceRmReq removes an instance. Without Confirm it is a dry run. It is
// refused while a rung's Upstream names the instance or its VPN client exists,
// unless Cascade: then every such Upstream is CLEARED (the rung is kept) and
// the VPN client is removed from wg0.conf.
type HZInstanceRmReq struct {
	Name    string `json:"name"`
	Cascade bool   `json:"cascade,omitempty"`
	Confirm bool   `json:"confirm,omitempty"`
}
