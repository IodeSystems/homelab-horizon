package apitypes

// TLS for a nested hz's own hostnames. The PARENT owns the zone and issues;
// the child pulls ISSUED material with its instance token:
//
//	GET /api/v1/hz-instances/<name>/cert   that instance's token only   InstanceCertResp
//
// ETag is the quoted sha256; If-None-Match answers 304; every answer carries
// Cache-Control: no-store. 404 is "not issued (yet)", 409 is "the certificate
// on file does not cover the declared domains — a reissue is pending"; the
// child keeps the certificate it has on both.
//
// WHAT CROSSES: one certificate, its issuer chain and its private key, for the
// instance's declared domains. WHAT NEVER CROSSES: the ACME account key and
// the DNS provider credential — the power to issue. The response has no field
// for either (internal/server TestInstanceCertRespCarriesNoIssuingPower).

// HZInstanceCertSuffix follows HZInstancesPath + "/<name>".
const HZInstanceCertSuffix = "/cert"

// InstanceCertResp is the pulled certificate. SHA256 is the hex sha256 of
// CertPEM + ChainPEM + KeyPEM, the exact bytes of the HAProxy bundle the child
// writes, in that order.
type InstanceCertResp struct {
	Instance string   `json:"instance"`
	Domains  []string `json:"domains"`
	CertPEM  string   `json:"cert_pem"`
	ChainPEM string   `json:"chain_pem"`
	KeyPEM   string   `json:"key_pem"`
	NotAfter string   `json:"not_after"` // RFC3339
	SHA256   string   `json:"sha256"`
}

// InstanceCertStatusResp is what the PARENT shows of an instance's
// certificate on its Instances page. No key material.
type InstanceCertStatusResp struct {
	// Issued: a certificate is on file. NotAfter, SANs and SHA256 describe it.
	Issued   bool     `json:"issued"`
	NotAfter int64    `json:"notAfter,omitempty"`
	SANs     []string `json:"sans,omitempty"`
	SHA256   string   `json:"sha256,omitempty"`
	// Unreadable is set when the file exists but could not be read or
	// parsed — not the same as "not issued".
	Unreadable string `json:"unreadable,omitempty"`
	// LastAttemptAt / LastError are this process's last issuance attempt
	// (memory: a restart forgets them). LastError "" with LastAttemptAt set
	// is a success.
	LastAttemptAt int64  `json:"lastAttemptAt,omitempty"`
	LastError     string `json:"lastError,omitempty"`
	// LastPulledAt / LastPulledSHA256: the child's last fetch of the cert
	// endpoint (200 or 304) since this process started; 0 is "not since".
	LastPulledAt     int64  `json:"lastPulledAt,omitempty"`
	LastPulledSHA256 string `json:"lastPulledSha256,omitempty"`
}

// ParentCertResp is what a CHILD shows of the certificate its parent issued:
// present on the self row of /api/v1/instances when this hz has an upstream.
type ParentCertResp struct {
	// Parent is upstream.url; Instance is upstream.instance ("" = no
	// certificate is pulled).
	Parent   string `json:"parent"`
	Instance string `json:"instance"`
	// Installed: a bundle is in the HAProxy cert dir. NotAfter / SANs /
	// SHA256 describe it.
	Installed bool     `json:"installed"`
	NotAfter  int64    `json:"notAfter,omitempty"`
	SANs      []string `json:"sans,omitempty"`
	SHA256    string   `json:"sha256,omitempty"`
	// LastCheckAt / LastError: the last pull attempt this process made.
	LastCheckAt int64  `json:"lastCheckAt,omitempty"`
	LastError   string `json:"lastError,omitempty"`
}
