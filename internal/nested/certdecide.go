package nested

import (
	"crypto"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
)

// THE PURE HALF of the child's certificate pull (CLAUDE.md #12): whether a
// certificate the parent sent may be installed, decided from its bytes and an
// instant passed in. cert.go fetches, writes and reloads.
//
// THE CHILD NEVER ISSUES. The parent owns the zone and the DNS credential; it
// sends a certificate and its key for this hz's declared domains, and nothing
// that could mint another (apitypes.InstanceCertResp has no field for it).
//
// A CERTIFICATE ON DISK IS KEPT, HOWEVER OLD (CLAUDE.md #5). Nothing here
// removes an installed bundle: a refused, unreachable or expired answer
// leaves it in place and serving. An expired certificate is LOUD, never
// dropped — a browser warning is recoverable, a :443 that stopped answering
// is an outage nobody is paged for.

// CertWarnSeconds is how close to not_after a certificate is LOUD at receipt.
// The parent renews 30 days out (certRenewalDays), so a certificate inside 14
// days is one the parent failed to renew.
const CertWarnSeconds int64 = 14 * 24 * 3600

// CertCheck is a certificate that passed: the bundle to write and what it is.
type CertCheck struct {
	// Bundle is CertPEM + ChainPEM + KeyPEM — the file HAProxy loads.
	Bundle   []byte
	SHA256   string
	NotAfter int64 // unix seconds
	SANs     []string
	// Loud is set when it is valid but close to expiry.
	Loud string
}

// BundleSHA256 is the hex sha256 of the bundle bytes.
func BundleSHA256(bundle []byte) string {
	sum := sha256.Sum256(bundle)
	return hex.EncodeToString(sum[:])
}

// CheckCert decides whether resp may be installed by the hz instance named
// instance, at nowUnix. It refuses — with a sentence — a body whose sha256
// does not match, a certificate for another instance, a key that does not
// match the certificate, SANs that are not exactly the declared domains, and
// a certificate already expired at receipt.
func CheckCert(instance string, resp apitypes.InstanceCertResp, nowUnix int64) (CertCheck, error) {
	if resp.Instance != instance {
		return CertCheck{}, fmt.Errorf("the parent sent a certificate for instance %q; this hz is %q", resp.Instance, instance)
	}
	bundle := []byte(resp.CertPEM + resp.ChainPEM + resp.KeyPEM)
	sha := BundleSHA256(bundle)
	if sha != resp.SHA256 {
		return CertCheck{}, fmt.Errorf("the bundle hashes to %s, not the sha256 the parent sent (%s)", sha, resp.SHA256)
	}
	block, _ := pem.Decode([]byte(resp.CertPEM))
	if block == nil || block.Type != "CERTIFICATE" {
		return CertCheck{}, errors.New("cert_pem holds no CERTIFICATE block")
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return CertCheck{}, fmt.Errorf("cert_pem does not parse: %w", err)
	}
	if err := keyMatches(leaf, []byte(resp.KeyPEM)); err != nil {
		return CertCheck{}, err
	}
	if missing, extra := diffNames(resp.Domains, leaf.DNSNames); len(resp.Domains) == 0 || len(missing) > 0 || len(extra) > 0 {
		return CertCheck{}, fmt.Errorf("the certificate names %s, but the names this hz serves for the parent are %s (missing %s, extra %s)",
			list(leaf.DNSNames), list(resp.Domains), list(missing), list(extra))
	}
	notAfter := leaf.NotAfter.Unix()
	if notAfter <= nowUnix {
		return CertCheck{}, fmt.Errorf("the certificate expired at %s, before it was received", leaf.NotAfter.UTC().Format("2006-01-02T15:04:05Z"))
	}
	c := CertCheck{Bundle: bundle, SHA256: sha, NotAfter: notAfter, SANs: append([]string(nil), leaf.DNSNames...)}
	if notAfter-nowUnix < CertWarnSeconds {
		c.Loud = "the certificate from the parent expires in " + strconv.FormatInt((notAfter-nowUnix)/3600, 10) +
			"h (" + leaf.NotAfter.UTC().Format("2006-01-02T15:04:05Z") + "); the parent renews 30 days out, so its renewal is failing — check the parent's instance page"
	}
	return c, nil
}

// NeedsInstall: write and reload only when the bundle changed.
func NeedsInstall(installedSHA string, c CertCheck) bool { return installedSHA != c.SHA256 }

// InstalledLoud is the line to log about the bundle already installed, ""
// when there is nothing to say. It never says remove it.
func InstalledLoud(notAfterUnix, nowUnix int64) string {
	switch {
	case notAfterUnix == 0:
		return ""
	case notAfterUnix <= nowUnix:
		return "the installed certificate from the parent has EXPIRED; KEEPING it and serving it rather than dropping TLS — the parent has not sent a newer one"
	case notAfterUnix-nowUnix < CertWarnSeconds:
		return "the installed certificate from the parent expires within 14 days and the parent has not sent a newer one"
	}
	return ""
}

func keyMatches(leaf *x509.Certificate, keyPEM []byte) error {
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return errors.New("key_pem holds no PEM block")
	}
	var key any
	var err error
	switch block.Type {
	case "RSA PRIVATE KEY":
		key, err = x509.ParsePKCS1PrivateKey(block.Bytes)
	case "EC PRIVATE KEY":
		key, err = x509.ParseECPrivateKey(block.Bytes)
	default:
		key, err = x509.ParsePKCS8PrivateKey(block.Bytes)
	}
	if err != nil {
		return fmt.Errorf("key_pem does not parse: %w", err)
	}
	signer, ok := key.(crypto.Signer)
	if !ok {
		return errors.New("key_pem is not a signing key")
	}
	pub, ok := signer.Public().(interface{ Equal(crypto.PublicKey) bool })
	if !ok || !pub.Equal(leaf.PublicKey) {
		return errors.New("the key does not match the certificate — refusing to install a bundle HAProxy would refuse to load")
	}
	return nil
}

func diffNames(want, have []string) (missing, extra []string) {
	w := map[string]bool{}
	for _, n := range want {
		w[strings.ToLower(n)] = true
	}
	h := map[string]bool{}
	for _, n := range have {
		h[strings.ToLower(n)] = true
	}
	for n := range w {
		if !h[n] {
			missing = append(missing, n)
		}
	}
	for n := range h {
		if !w[n] {
			extra = append(extra, n)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	return missing, extra
}

func list(names []string) string {
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}
