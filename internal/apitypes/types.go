package apitypes

import "time"

// Error response
type ErrorResponse struct {
	Error string `json:"error"`
}

// OK response (used by many mutation endpoints)
type OKResponse struct {
	OK bool `json:"ok"`
}

// Dashboard

type DashboardResponse struct {
	ServiceCount   int    `json:"serviceCount"`
	DomainCount    int    `json:"domainCount"`
	ZoneCount      int    `json:"zoneCount"`
	PeerCount      int    `json:"peerCount"`
	HAProxyRunning bool   `json:"haproxyRunning"`
	SSLEnabled     bool   `json:"sslEnabled"`
	Version        string `json:"version"`
	ChecksTotal    int    `json:"checksTotal"`
	ChecksHealthy  int    `json:"checksHealthy"`
	ChecksFailed   int    `json:"checksFailed"`

	// PeerSync is populated only when this instance is a non-primary in a fleet.
	PeerSync *PeerSyncStatus `json:"peerSync,omitempty"`
}

// PeerSyncStatus surfaces the peer-sync pull-loop state to the dashboard so
// operators can spot a non-primary that has stopped converging without
// having to scrape logs. See plan/plan.md Phase 1 hardening item 5.
type PeerSyncStatus struct {
	PrimaryID     string `json:"primaryId"`               // peer id this instance is pulling from
	PullCount     int    `json:"pullCount"`               // total pull attempts since startup
	LastPullAt    int64  `json:"lastPullAt,omitempty"`    // unix seconds, 0 if never
	LastSuccessAt int64  `json:"lastSuccessAt,omitempty"` // unix seconds, 0 if never
	LastApplyAt   int64  `json:"lastApplyAt,omitempty"`   // unix seconds, 0 if never
	LastError     string `json:"lastError,omitempty"`     // empty if last attempt succeeded
}

// Services

type HealthCheckResp struct {
	Path string `json:"path"`
}

type DeployResp struct {
	NextBackend string `json:"nextBackend"`
	ActiveSlot  string `json:"activeSlot"`
	Balance     string `json:"balance"`
}

type ProxyResp struct {
	Backend             string           `json:"backend"`
	HealthCheck         *HealthCheckResp `json:"healthCheck,omitempty"`
	InternalOnly        bool             `json:"internalOnly"`
	Deploy              *DeployResp      `json:"deploy,omitempty"`
	MaintenancePageMD5  string           `json:"maintenancePageMD5,omitempty"`
}

type InternalDNSResp struct {
	IP string `json:"ip"`
}

type ExternalDNSResp struct {
	IP  string   `json:"ip"`
	IPs []string `json:"ips,omitempty"` // All IPs for round-robin DNS
	TTL int      `json:"ttl"`
}

type ServiceStatus struct {
	InternalDNSUp       bool   `json:"internalDNSUp"`                 // dnsmasq resolves the primary domain
	InternalDNSResolved string `json:"internalDNSResolved,omitempty"` // actual IP from dnsmasq
	ExternalDNSUp       bool   `json:"externalDNSUp"`                 // 1.1.1.1 resolves the primary domain
	ExternalDNSResolved string `json:"externalDNSResolved,omitempty"` // actual IP from 1.1.1.1
	ProxyUp             bool   `json:"proxyUp"`                       // HAProxy backend healthy (from HAProxy socket)
	ProxyError          string `json:"proxyError,omitempty"`          // health check error
	ProxyState          string `json:"proxyState,omitempty"`          // "up", "down", "drain", "maint" (current/single backend)
	ProxyNextState      string `json:"proxyNextState,omitempty"`      // "up", "down", "drain", "maint" (next slot for deploy)
}

type ServiceResp struct {
	Name        string           `json:"name"`
	Domains     []string         `json:"domains"`
	InternalDNS *InternalDNSResp `json:"internalDNS,omitempty"`
	ExternalDNS *ExternalDNSResp `json:"externalDNS,omitempty"`
	Proxy       *ProxyResp       `json:"proxy,omitempty"`
	Status      ServiceStatus    `json:"status"`
}

// Domains

type DomainResp struct {
	Domain               string `json:"domain"`
	ZoneName             string `json:"zoneName"`
	ZoneHasSSL           bool   `json:"zoneHasSSL"`
	HasZone              bool   `json:"hasZone"`
	ServiceName          string `json:"serviceName"`
	HasService           bool   `json:"hasService"`
	HasInternalDNS       bool   `json:"hasInternalDNS"`
	InternalIP           string `json:"internalIP"`
	HasExternalDNS       bool     `json:"hasExternalDNS"`
	ExternalIP           string   `json:"externalIP"`
	ExternalIPs          []string `json:"externalIPs,omitempty"` // All IPs for round-robin DNS
	DnsmasqResolvedIP    string `json:"dnsmasqResolvedIP"`
	RemoteResolvedIP     string `json:"remoteResolvedIP"`
	DnsmasqDNSMatch      bool   `json:"dnsmasqDNSMatch"`
	RemoteDNSMatch       bool   `json:"remoteDNSMatch"`
	HasProxy             bool   `json:"hasProxy"`
	ProxyBackend         string `json:"proxyBackend"`
	InternalOnly         bool   `json:"internalOnly"`
	HasHealthCheck       bool   `json:"hasHealthCheck"`
	HealthPath           string `json:"healthPath"`
	HasSSLCoverage       bool   `json:"hasSSLCoverage"`
	CertExists           bool   `json:"certExists"`
	CertExpiry           string `json:"certExpiry"`
	CertDomain           string `json:"certDomain"`
	CanEnableHTTPS       bool   `json:"canEnableHTTPS"`
	NeededSubZone        string `json:"neededSubZone"`
	NeededSubZoneDisplay string `json:"neededSubZoneDisplay"`
	CanRequestCert       bool     `json:"canRequestCert"`
	CanSyncDNS           bool     `json:"canSyncDNS"`
	CoveredBy            string   `json:"coveredBy,omitempty"`     // wildcard that covers this domain (e.g., "*.iodesystems.com")
	IsRedundant          bool     `json:"isRedundant"`             // SubZone is redundant with a wildcard on the same zone
	AbsorbedDomains      []AbsorbedDomain `json:"absorbedDomains,omitempty"` // for wildcards: service domains this covers
}

type AbsorbedDomain struct {
	Domain  string `json:"domain"`
	Service string `json:"service,omitempty"` // service name if bound
}

type SSLGapResp struct {
	Domain   string `json:"domain"`
	ZoneName string `json:"zoneName"`
	SubZone  string `json:"subZone"`
	Display  string `json:"display"`
	Reason   string `json:"reason"`
}

type ZoneSSLResp struct {
	ZoneName          string   `json:"zoneName"`
	SSLEnabled        bool     `json:"sslEnabled"`
	ConfiguredDomains []string `json:"configuredDomains"`
	ActualSANs        []string `json:"actualSANs"`
	CertExists        bool     `json:"certExists"`
	CertExpiry        string   `json:"certExpiry"`
	CertIssuer        string   `json:"certIssuer"`
	MissingSANs       []string `json:"missingSANs"`
	ExtraSANs         []string `json:"extraSANs"`
}

type DomainsResponse struct {
	Domains         []DomainResp  `json:"domains"`
	TotalCount      int           `json:"totalCount"`
	IntDNSCount     int           `json:"intDNSCount"`
	ExtDNSCount     int           `json:"extDNSCount"`
	HTTPSCount      int           `json:"httpsCount"`
	ProxyCount      int           `json:"proxyCount"`
	SSLGaps         []SSLGapResp  `json:"sslGaps"`
	ZoneSSLStatuses []ZoneSSLResp `json:"zoneSSLStatuses"`
}

// VPN Peers

type PeerResp struct {
	Name            string `json:"name"`
	PublicKey       string `json:"publicKey"`
	AllowedIPs      string `json:"allowedIPs"`
	Endpoint        string `json:"endpoint,omitempty"`
	LatestHandshake string `json:"latestHandshake,omitempty"`
	TransferRx      string `json:"transferRx,omitempty"`
	TransferTx      string `json:"transferTx,omitempty"`
	Online          bool   `json:"online"`
	IsAdmin         bool   `json:"isAdmin"`
	Profile         string `json:"profile"`
	MFAEnrolled     bool   `json:"mfaEnrolled"`
	MFASessionActive bool  `json:"mfaSessionActive"`
	MFASessionExpiry string `json:"mfaSessionExpiry,omitempty"`
}

// MFA types

type MFAStatusResponse struct {
	Enrolled       bool     `json:"enrolled"`
	SessionActive  bool     `json:"sessionActive"`
	SessionExpiry  string   `json:"sessionExpiry,omitempty"`
	Durations      []string `json:"durations"`
	ProvisioningURI string  `json:"provisioningUri,omitempty"` // only during enrollment
}

type MFAEnrollResponse struct {
	OK              bool   `json:"ok"`
	ProvisioningURI string `json:"provisioningUri"`
	Secret          string `json:"secret"`
}

type MFAVerifyRequest struct {
	Code     string `json:"code"`
	Duration string `json:"duration"` // "2h", "4h", "8h", "forever"
}

type MFAVerifyResponse struct {
	OK     bool   `json:"ok"`
	Expiry string `json:"expiry,omitempty"`
}

type MFASettingsResponse struct {
	Enabled   bool     `json:"enabled"`
	Durations []string `json:"durations"`
}

type AddPeerResponse struct {
	OK     bool   `json:"ok"`
	Config string `json:"config"`
	QRCode string `json:"qrCode"`
}

type PeerConfigResponse struct {
	OK     bool   `json:"ok"`
	Config string `json:"config"`
	QRCode string `json:"qrCode"`
}

type RekeyPeerResponse struct {
	OK        bool   `json:"ok"`
	Config    string `json:"config"`
	QRCode    string `json:"qrCode"`
	ShareToken string `json:"shareToken"`
	ShareURL   string `json:"shareURL"`
}

type ConfigShareResp struct {
	Token    string `json:"token"`
	URL      string `json:"url"`
	PeerName string `json:"peerName"`
}

// Invites

type InviteResp struct {
	Token string `json:"token"`
	URL   string `json:"url"`
}

type CreateInviteResponse struct {
	OK    bool   `json:"ok"`
	Token string `json:"token"`
	URL   string `json:"url"`
}

// Zones

type ZoneResp struct {
	Name         string   `json:"name"`
	ZoneID       string   `json:"zoneId"`
	SSLEnabled   bool     `json:"sslEnabled"`
	SSLEmail     string   `json:"sslEmail,omitempty"`
	SubZones     []string `json:"subZones"`
	ProviderType string   `json:"providerType,omitempty"`
}

// Settings

type HAProxyResp struct {
	Running      bool   `json:"running"`
	ConfigExists bool   `json:"configExists"`
	Version      string `json:"version"`
	Enabled      bool   `json:"enabled"`
	HTTPPort     int    `json:"httpPort"`
	HTTPSPort    int    `json:"httpsPort"`
}

type SSLResp struct {
	Enabled        bool   `json:"enabled"`
	CertDir        string `json:"certDir"`
	HAProxyCertDir string `json:"haproxyCertDir"`
}

type CheckStatusResp struct {
	Name      string    `json:"name"`
	Type      string    `json:"type"`
	Target    string    `json:"target"`
	Status    string    `json:"status"`
	LastCheck time.Time `json:"last_check"`
	LastError string    `json:"last_error,omitempty"`
	Interval  int       `json:"interval"`
	Enabled   bool      `json:"enabled"`
	AutoGen   bool      `json:"auto_gen"`
}

type ConfigResp struct {
	PublicIP       string   `json:"publicIP"`
	LocalInterface string   `json:"localInterface"`
	DnsmasqEnabled bool     `json:"dnsmasqEnabled"`
	VPNAdmins      []string `json:"vpnAdmins"`
}

type SettingsResponse struct {
	Zones   []ZoneResp        `json:"zones"`
	HAProxy HAProxyResp       `json:"haproxy"`
	SSL     SSLResp           `json:"ssl"`
	Checks  []CheckStatusResp `json:"checks"`
	Config  ConfigResp        `json:"config"`
}

// HAProxy config preview

type HAProxyConfigPreview struct {
	Config string `json:"config"`
}

// Auth

type AuthStatusResponse struct {
	Authenticated bool   `json:"authenticated"`
	Method        string `json:"method,omitempty"`

	// Multi-instance HA — populated when this instance is part of a fleet.
	PeerID        string `json:"peerId,omitempty"`        // local instance identity
	ConfigPrimary bool   `json:"configPrimary,omitempty"` // true if this instance is the config primary
	PrimaryID     string `json:"primaryId,omitempty"`     // peer id of the config primary (when this instance is non-primary)
}

type LoginRequest struct {
	Token string `json:"token"`
}

type LoginResponse struct {
	OK       bool   `json:"ok"`
	Invite   bool   `json:"invite,omitempty"`
	Redirect string `json:"redirect,omitempty"`
}

// Deploy

type DeploySlotStatus struct {
	Slot    string `json:"slot"`
	Backend string `json:"backend"`
	State   string `json:"state"`
}

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

type DeployStateChangeResponse struct {
	Status string `json:"status"`
	Server string `json:"server"`
	State  string `json:"state"`
}

type DeploySwapResponse struct {
	Status     string `json:"status"`
	ActiveSlot string `json:"active_slot"`
	Current    string `json:"current"`
	Next       string `json:"next"`
}

// Service mutations

type ServiceRequest struct {
	OriginalName string `json:"originalName,omitempty"`
	Name         string `json:"name"`
	Domains      []string `json:"domains"`
	InternalDNS  *ServiceRequestInternalDNS `json:"internalDNS,omitempty"`
	ExternalDNS  *ServiceRequestExternalDNS `json:"externalDNS,omitempty"`
	Proxy        *ServiceRequestProxy       `json:"proxy,omitempty"`
}

type ServiceRequestInternalDNS struct {
	IP string `json:"ip"`
}

type ServiceRequestExternalDNS struct {
	IP  string   `json:"ip"`
	IPs []string `json:"ips,omitempty"` // Multiple IPs for round-robin DNS
	TTL int      `json:"ttl"`
}

type ServiceRequestProxy struct {
	Backend     string                      `json:"backend"`
	HealthCheck *ServiceRequestHealthCheck  `json:"healthCheck,omitempty"`
	InternalOnly bool                       `json:"internalOnly"`
	Deploy      *ServiceRequestDeploy       `json:"deploy,omitempty"`
}

type ServiceRequestDeploy struct {
	NextBackend string `json:"nextBackend"`
	Balance     string `json:"balance,omitempty"` // "first" or "roundrobin"
}

type ServiceRequestHealthCheck struct {
	Path string `json:"path"`
}

// IP Ban types

type BanRequest struct {
	IP      string `json:"ip"`
	Timeout int    `json:"timeout,omitempty"` // seconds, 0 = permanent
	Reason  string `json:"reason,omitempty"`
}

type UnbanRequest struct {
	IP string `json:"ip"`
}

type BanEntry struct {
	IP        string `json:"ip"`
	Timeout   int    `json:"timeout,omitempty"`
	CreatedAt int64  `json:"createdAt"`
	ExpiresAt int64  `json:"expiresAt,omitempty"`
	Reason    string `json:"reason,omitempty"`
	Service   string `json:"service,omitempty"`
}

type BanListResponse struct {
	Bans []BanEntry `json:"bans"`
}

// DNS sync

type DNSSyncResponse struct {
	OK      bool `json:"ok"`
	Changed bool `json:"changed"`
}

type DNSSyncAllResponse struct {
	OK      bool `json:"ok"`
	Updated int  `json:"updated"`
	Failed  int  `json:"failed"`
}

// Toggle admin

type ToggleAdminResponse struct {
	OK      bool `json:"ok"`
	IsAdmin bool `json:"isAdmin"`
}

// Trigger sync

type TriggerSyncResponse struct {
	OK      bool `json:"ok"`
	Started bool `json:"started"`
}

// Run check

type RunCheckResponse struct {
	OK     bool   `json:"ok"`
	Status string `json:"status"`
}

// Check history

type CheckResult struct {
	Timestamp time.Time `json:"timestamp"`
	Status    string    `json:"status"`
	Latency   int64     `json:"latency"`
	Error     string    `json:"error,omitempty"`
}

type CheckHistoryResponse struct {
	Name    string        `json:"name"`
	Results []CheckResult `json:"results"`
}

type ChecksOverview struct {
	Total   int `json:"total"`
	Healthy int `json:"healthy"`
	Failed  int `json:"failed"`
	Pending int `json:"pending"`
}

// Domain SSL mutations

type DomainSSLAddRequest struct {
	Domain string `json:"domain"`
}

type DomainSSLAddResponse struct {
	OK      bool   `json:"ok"`
	Zone    string `json:"zone"`
	SubZone string `json:"subZone"`
}

type DomainSSLRemoveRequest struct {
	Domain string `json:"domain"`
}

// Service integration

type ServiceIntegration struct {
	Name      string `json:"name"`
	Token     string `json:"token"`
	BaseURL   string `json:"baseURL"`
	HasDeploy bool   `json:"hasDeploy"`
}

// HA Fleet

type HAFleetPeer struct {
	ID           string `json:"id"`
	WGAddr       string `json:"wgAddr"`
	Primary      bool   `json:"primary,omitempty"`
	Online       bool   `json:"online,omitempty"`
	LastSyncAt   string `json:"lastSyncAt,omitempty"`
	LastSyncErr  string `json:"lastSyncErr,omitempty"`
}

type HAStatusResponse struct {
	PeerID        string        `json:"peerId"`
	ConfigPrimary bool          `json:"configPrimary"`
	Peers         []HAFleetPeer `json:"peers"`
}

type HACreateJoinTokenRequest struct {
	PeerID   string `json:"peerId"`
	Topology string `json:"topology"` // "same-subnet" or "site-to-site"
	// Site-to-site fields
	RemoteEndpoint string `json:"remoteEndpoint,omitempty"` // e.g. "1.2.3.4:51830"
	VPNRange       string `json:"vpnRange,omitempty"`       // e.g. "10.0.2.0/24"
}

type HACreateJoinTokenResponse struct {
	OK       bool   `json:"ok"`
	Token    string `json:"token"`
	OneLiner string `json:"oneLiner"`
}

type HAJoinCompleteRequest struct {
	PeerID    string `json:"peer_id"`
	WGAddr    string `json:"wg_addr"`
	S2SPubKey string `json:"s2s_pubkey,omitempty"`
	VPNPubKey string `json:"vpn_pubkey"`
}
