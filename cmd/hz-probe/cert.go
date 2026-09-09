package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"flag"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// hostList collects repeated --host flags.
type hostList []string

func (h *hostList) String() string     { return fmt.Sprint(*h) }
func (h *hostList) Set(v string) error { *h = append(*h, v); return nil }

// runGenCert writes a self-signed certificate and key.
//
// This exists because the alternative an operator reaches for is serving
// plain HTTP, which puts the shared token on the public internet in
// cleartext. hz does not have to trust this certificate's issuer: it pins the
// exact certificate by fingerprint, which is a stronger guarantee than a
// public CA gives, for this one connection.
func runGenCert(args []string) error {
	fs := flag.NewFlagSet("gen-cert", flag.ExitOnError)
	var hosts hostList
	fs.Var(&hosts, "host", "IP or DNS name hz will connect to (repeatable; default: this host's addresses)")
	certPath := fs.String("tls-cert", defaultCertPath, "certificate file to write")
	keyPath := fs.String("tls-key", defaultKeyPath, "key file to write")
	days := fs.Int("days", 3650, "validity in days")
	force := fs.Bool("force", false, "overwrite an existing certificate")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if fileExists(*certPath) && !*force {
		return fmt.Errorf("%s already exists; pass --force to replace it "+
			"(hz's pin_sha256 will need updating if you do)", *certPath)
	}

	if len(hosts) == 0 {
		hosts = localAddresses()
		if len(hosts) == 0 {
			return fmt.Errorf("could not determine this host's addresses; pass --host")
		}
		fmt.Printf("No --host given; using this host's addresses: %v\n", []string(hosts))
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("generating a key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return fmt.Errorf("generating a serial: %w", err)
	}

	tmpl := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "hz-probe"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(0, 0, *days),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}

	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return fmt.Errorf("creating the certificate: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return fmt.Errorf("encoding the key: %w", err)
	}

	if err := writePEM(*certPath, "CERTIFICATE", der, 0o644); err != nil {
		return err
	}
	if err := writePEM(*keyPath, "EC PRIVATE KEY", keyDER, 0o600); err != nil {
		return err
	}

	sum := sha256.Sum256(der)
	fmt.Printf("Wrote %s and %s, valid %d days for %v\n", *certPath, *keyPath, *days, []string(hosts))
	fmt.Printf("\nhz must pin this certificate. In hz's \"remote_probes\" entry:\n")
	fmt.Printf("  \"pin_sha256\": %q\n", hex.EncodeToString(sum[:]))
	return nil
}

// writePEM writes one PEM block, replacing whatever was there.
func writePEM(path, blockType string, der []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	if err := pem.Encode(f, &pem.Block{Type: blockType, Bytes: der}); err != nil {
		return fmt.Errorf("encoding %s: %w", path, err)
	}
	// Mode is only applied on creation, so set it explicitly for the case
	// where the file already existed with looser permissions.
	return os.Chmod(path, mode)
}

// localAddresses returns this host's routable addresses, which is what hz
// would connect to when nobody says otherwise.
func localAddresses() []string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var out []string
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok || ipnet.IP.IsLoopback() || ipnet.IP.IsLinkLocalUnicast() {
			continue
		}
		out = append(out, ipnet.IP.String())
	}
	return out
}

func runFingerprint(args []string) error {
	fs := flag.NewFlagSet("fingerprint", flag.ExitOnError)
	certPath := fs.String("tls-cert", defaultCertPath, "certificate to fingerprint")
	if err := fs.Parse(args); err != nil {
		return err
	}
	pin, err := fingerprintOf(*certPath)
	if err != nil {
		return err
	}
	fmt.Println(pin)
	return nil
}

// fingerprintOf is the SHA-256 of a certificate's DER, which is the value
// hz's pin_sha256 compares against.
func fingerprintOf(certPath string) (string, error) {
	pemBytes, err := os.ReadFile(certPath)
	if err != nil {
		return "", fmt.Errorf("could not read %s: %w", certPath, err)
	}
	der, err := firstCertDER(pemBytes)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:]), nil
}

// firstCertDER returns the DER of the first CERTIFICATE block in a PEM file,
// which is the leaf hz pins.
func firstCertDER(pemBytes []byte) ([]byte, error) {
	rest := pemBytes
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			return nil, fmt.Errorf("no CERTIFICATE block in the file")
		}
		if block.Type == "CERTIFICATE" {
			return block.Bytes, nil
		}
	}
}
