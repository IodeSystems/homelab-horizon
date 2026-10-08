package apitypes

// The one-command bootstrap of a nested hz (handlers_nested_bootstrap.go).
//
//	POST /api/v1/nested/bootstrap            admin            declare + mint a grant, return the command
//	GET  /admin/hz-child/install/<grant>     public vhost     the script, for a live grant
//	POST /admin/hz-child/enrol/<grant>       public vhost     the box's PUBLIC key up; wg peer + token down
//	GET  /api/v1/nested/bin/<os>-<arch>      instance token   the server binary, over the upstream VPN
//
// What crosses: the box's WireGuard PUBLIC key goes up. The instance token, the
// wg peer details and the seed config come down. The private key is generated
// on the box and never leaves it (CLAUDE.md #4).

// NestedBootstrapPath is the generate endpoint.
const NestedBootstrapPath = "/api/v1/nested/bootstrap"

// NestedBootstrapReq declares a nested hz for a project and asks for the
// command that provisions it.
//
// Instance is the hz instance the command declares (config.HZInstance, never a
// Machine). Machine is its pre-B3 spelling, accepted one release; sending both
// with different values is refused.
//
// Rung is the rung's NAME in Project; the rung is declared as Project/Rung with
// Upstream = Instance. From is an existing rung of the same project that
// promotes into it. Posture defaults to prod.
//
// Platform is optional: "linux-amd64" or "linux-arm64" when the operator
// knows the box's architecture. With it, a missing binary for that platform is
// a refusal; without it, a missing one is a warning on the response and the
// script stops on such a box before it touches anything.
//
// URL is optional: where the child's hz will answer, a statement for the
// operator (HZInstance.URL). Empty on a new instance is "not known yet" — a
// .invalid placeholder — and enrolment replaces it with the box's VPN address.
//
// Domain is the instance's ONE registered domain (N4j): it is set on the
// instance (its names and DNS grant derive from it) and the kiosk is
// kiosk.hz.<Domain>, the endpoint kiosk.hz.<Domain>:51820, and the child's UI
// hz.<Domain>, VPN-only.
//
// HumanVPN is the child's OWN wg0 for people, its MFA kiosk and its listener
// on its wg0 address (N4g). nil is ON; false is "no human VPN" (the child is
// then reached by ssh tunnel only, as before). With it on, Domain (or the
// deprecated Kiosk) is required. Kiosk is the pre-N4j spelling, one release:
// it sets only the kiosk's hostname (kiosk_url https://<Kiosk>, the endpoint
// <Kiosk>:51820), added to the instance's explicit Domains and DNS grant.
// Sending both is refused unless Kiosk is the derived kiosk.
// HumanVPNRange is optional: without it this hz proposes the next free
// 10.<n>.0.0/24 after its own VPN range.
type NestedBootstrapReq struct {
	Project       string `json:"project"`
	Instance      string `json:"instance,omitempty"`
	Machine       string `json:"machine,omitempty"`
	Rung          string `json:"rung"`
	From          string `json:"from"`
	Posture       string `json:"posture,omitempty"`
	Platform      string `json:"platform,omitempty"`
	URL           string `json:"url,omitempty"`
	HumanVPN      *bool  `json:"humanVpn,omitempty"`
	Domain        string `json:"domain,omitempty"`
	Kiosk         string `json:"kiosk,omitempty"`
	HumanVPNRange string `json:"humanVpnRange,omitempty"`
}

// NestedHumanVPN is the child's human VPN as the command sets it up: Range is
// its wg0 range (Proposed when this hz chose it), ServerAddress the child's
// own address on it (where its hz also listens), Kiosk, KioskURL and Endpoint
// what a person's client is given. Domain and UIURL (https://hz.<domain>/app/,
// VPN-only) are set when the command registered a domain.
type NestedHumanVPN struct {
	Range         string `json:"range"`
	Proposed      bool   `json:"proposed"`
	ServerAddress string `json:"serverAddress"`
	Domain        string `json:"domain,omitempty"`
	Kiosk         string `json:"kiosk"`
	KioskURL      string `json:"kioskUrl"`
	UIURL         string `json:"uiUrl,omitempty"`
	Endpoint      string `json:"endpoint"`
	// DomainsChanged is true when this request added the kiosk name to the
	// instance's domains or DNS grant (a certificate reissue follows).
	DomainsChanged bool `json:"domainsChanged"`
}

// NestedBootstrapResp is the generated command, shown once.
//
// Reused lists what already existed (a re-provision of a wiped box):
// "instance", "rung". Revoked is true when an unused earlier command for this
// instance was revoked by this one. Machine repeats Instance for a pre-B3
// client, one release.
type NestedBootstrapResp struct {
	Command   string `json:"command"`
	ExpiresAt int64  `json:"expiresAt"` // unix seconds
	Instance  string `json:"instance"`
	Machine   string `json:"machine"`
	Project   string `json:"project"`
	Rung      string `json:"rung"` // "<project>/<rung>"
	// Platforms is every platform a child binary is stored for; Missing is
	// the supported ones that are not. Warning is a sentence when Missing is
	// not empty — the UI shows it before the command.
	Platforms []string `json:"platforms"`
	Missing   []string `json:"missing"`
	Warning   string   `json:"warning,omitempty"`
	Reused    []string `json:"reused"`
	Revoked   bool     `json:"revoked"`
	// HumanVPN is nil when the request turned it off.
	HumanVPN *NestedHumanVPN `json:"humanVpn,omitempty"`
}

// NestedEnrolReq is what the box sends: its WireGuard PUBLIC key, and the
// platform it needs a binary for. Hostname is a label for the log only — the
// instance's identity is the grant's.
type NestedEnrolReq struct {
	PublicKey string `json:"public_key"`
	Platform  string `json:"platform"`
	Hostname  string `json:"hostname,omitempty"`
}

// NestedEnrolResp is everything the box needs, and nothing it does not. The
// instance token is shown here ONCE; the parent keeps its sha256.
type NestedEnrolResp struct {
	WG            NestedEnrolWG     `json:"wg"`
	ParentURL     string            `json:"parent_url"`
	InstanceToken string            `json:"instance_token"`
	Instance      string            `json:"instance"`
	Machine       string            `json:"machine"` // = Instance, for a pre-B3 reader
	Project       string            `json:"project"`
	Rung          string            `json:"rung"`
	ServiceName   string            `json:"service_name"`
	Binary        NestedEnrolBinary `json:"binary"`
	// Seed is the child's config.json seed: the project, the rung, a local
	// service for that rung (token empty — the BOX generates it and the
	// parent never sees it) and the upstream block.
	Seed NestedChildSeed `json:"seed"`
}

// NestedEnrolWG is the box's side of the upstream tunnel.
type NestedEnrolWG struct {
	Address       string `json:"address"` // the box's VPN address, no mask
	PeerPublicKey string `json:"peer_public_key"`
	Endpoint      string `json:"endpoint"`
	AllowedIPs    string `json:"allowed_ips"` // the parent's WG address /32 — nothing else
}

// NestedEnrolBinary is the server binary the box downloads over the VPN.
type NestedEnrolBinary struct {
	URL     string `json:"url"`
	SHA256  string `json:"sha256"`
	Version string `json:"version"`
}

// NestedChildSeed is the subset of the child's config.json the bootstrap
// writes. The json tags are config.Config's; TestEnrolSeedLoadsAsTheChildsConfig
// loads it through config.LoadFromJSON, so a tag that drifts fails there.
//
// The human-VPN fields (N4g) are set only when the command has a human VPN.
// services[0] is always the rung's service; services[1], when present, is the
// kiosk (proxy.self, internal_dns at the child's wg0 address, external_dns {}
// so the child asks this hz for its A record); services[2], with a domain, is
// hz-ui on hz.<domain> (proxy.self, internal_dns at the wg0 address, and NO
// external_dns: it is VPN-only).
type NestedChildSeed struct {
	Projects     []NestedSeedProject     `json:"projects"`
	Environments []NestedSeedEnvironment `json:"environments"`
	Services     []NestedSeedService     `json:"services"`
	Upstream     NestedSeedUpstream      `json:"upstream"`

	WGInterface    string `json:"wg_interface,omitempty"`
	WGConfigPath   string `json:"wg_config_path,omitempty"`
	VPNRange       string `json:"vpn_range,omitempty"`
	DNS            string `json:"dns,omitempty"`
	ServerEndpoint string `json:"server_endpoint,omitempty"`
	KioskURL       string `json:"kiosk_url,omitempty"`
	VPNListen      *bool  `json:"vpn_listen,omitempty"`
	VPNMFAEnabled  bool   `json:"vpn_mfa_enabled,omitempty"`
	VPNMFAScope    string `json:"vpn_mfa_scope,omitempty"`
	HAProxyEnabled bool   `json:"haproxy_enabled,omitempty"`
}

type NestedSeedProject struct {
	Name string `json:"name"`
}

type NestedSeedEnvironment struct {
	Project string `json:"project"`
	Name    string `json:"name"`
	Posture string `json:"posture"`
}

type NestedSeedService struct {
	Name        string                 `json:"name"`
	Project     string                 `json:"project"`
	Environment string                 `json:"environment"`
	Token       string                 `json:"token"`
	Domains     []string               `json:"domains"`
	InternalDNS *NestedSeedInternalDNS `json:"internal_dns,omitempty"`
	ExternalDNS *NestedSeedExternalDNS `json:"external_dns,omitempty"`
	Proxy       *NestedSeedProxy       `json:"proxy,omitempty"`
}

// NestedSeedInternalDNS is config.InternalDNS: what the child's dnsmasq
// answers for the kiosk name — the child's own wg0 address, so a VPN client
// reaches the kiosk THROUGH the tunnel (the jail knows a peer by its tunnel
// source address).
type NestedSeedInternalDNS struct {
	IP string `json:"ip"`
}

// NestedSeedExternalDNS is config.ExternalDNS, always empty here: no pin, so
// the record tracks the child's public IP.
type NestedSeedExternalDNS struct{}

// NestedSeedProxy is config.ProxyConfig's self route: HAProxy sends the kiosk
// name to the child's own UI, where the MFA portal is.
type NestedSeedProxy struct {
	Self bool `json:"self"`
}

type NestedSeedUpstream struct {
	URL         string           `json:"url"`
	TokenFile   string           `json:"token_file"`
	Rungs       []NestedSeedRung `json:"rungs"`
	PollSeconds int              `json:"poll_seconds,omitempty"`
	// Instance is the child's name here, so it can pull the certificate this
	// hz issues for its domains (/api/v1/hz-instances/<instance>/cert).
	Instance string `json:"instance,omitempty"`
	// Domain is the instance's registered domain (config.UpstreamHZ.Domain).
	Domain string `json:"domain,omitempty"`
}

type NestedSeedRung struct {
	Project     string `json:"project"`
	Environment string `json:"environment"`
}

// Nested instance states, as the Instances and project pages show them
// (InstanceNestedResp.State).
const (
	// NestedStateDeclared: the machine is declared; nothing provisioned.
	NestedStateDeclared = "declared"
	// NestedStateCommandGenerated: a bootstrap command is live (GrantExpiresAt).
	NestedStateCommandGenerated = "command-generated"
	// NestedStateEnrolled: the box enrolled (EnrolledAt) and has not pulled since.
	NestedStateEnrolled = "enrolled"
	// NestedStateConnected: the instance has pulled a desired state (LastPullAt).
	NestedStateConnected = "connected"
	// NestedStateNeverConnected: hz holds a token and a VPN link for it, and
	// has no record of a pull — and no enrolment this hz remembers.
	NestedStateNeverConnected = "never-connected"
)
