package letsencrypt

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"
)

// The cert read used to be five `openssl x509` calls. It is crypto/x509 now,
// and the strings the API and UI carry are kept as openssl printed them —
// checked against openssl on a real Let's Encrypt certificate (2026-10-30).
func TestCertInfoReadsAsOpensslPrinted(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(0x3ED704A1), // odd hex length: openssl pads it
		Subject:      pkix.Name{CommonName: "example.com"},
		Issuer:       pkix.Name{Country: []string{"US"}, Organization: []string{"Let's Encrypt"}, CommonName: "R11"},
		DNSNames:     []string{"example.com", "www.example.com"},
		NotBefore:    time.Date(2026, 10, 30, 18, 29, 35, 0, time.UTC),
		NotAfter:     time.Date(2027, 1, 6, 1, 36, 54, 0, time.UTC),
	}
	parent := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: tmpl.Issuer}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	// A bundle with the key first: the leaf certificate is what is read.
	keyDER, _ := x509.MarshalECPrivateKey(key)
	bundle := append(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})...)

	got := CertInfoFromPEM(bundle)
	want := CertInfo{
		Domain:    "example.com",
		SANs:      []string{"example.com", "www.example.com"},
		Issuer:    "C=US, O=Let's Encrypt, CN=R11",
		NotBefore: "Oct 30 18:29:35 2026 GMT",
		NotAfter:  "Jan  6 01:36:54 2027 GMT", // a space-padded day, as openssl prints it
		Serial:    "3ED704A1",
	}
	if got.Domain != want.Domain || got.Issuer != want.Issuer || got.NotBefore != want.NotBefore ||
		got.NotAfter != want.NotAfter || got.Serial != want.Serial || len(got.SANs) != 2 ||
		got.SANs[0] != want.SANs[0] || got.SANs[1] != want.SANs[1] {
		t.Errorf("CertInfoFromPEM =\n%+v\nwant\n%+v", *got, want)
	}
	if e := CertEndDate(bundle); e != want.NotAfter {
		t.Errorf("CertEndDate = %q, want %q", e, want.NotAfter)
	}
	if e := CertEndDate([]byte("not a pem")); e != "" {
		t.Errorf("CertEndDate(garbage) = %q, want empty", e)
	}
	if got := CertInfoFromPEM(nil); got.Domain != "" || got.NotAfter != "" {
		t.Errorf("CertInfoFromPEM(nil) = %+v, want empty", *got)
	}
}
