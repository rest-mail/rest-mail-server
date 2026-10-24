package dns

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"
)

func tlsaTestCert(t *testing.T, seed byte, serial int64) []byte {
	t.Helper()
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seed}, 32))
	template := &x509.Certificate{
		SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "mx.example.test"},
		DNSNames: []string{"mx.example.test"}, NotBefore: time.Unix(0, 0),
		NotAfter: time.Unix(serial*86400, 0),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestSMTPTLSARecordsMatchOpenSSLSPKI(t *testing.T) {
	cert := tlsaTestCert(t, 1, 1)
	records, err := SMTPTLSARecords("mx.example.test.", cert)
	if err != nil || len(records) != 1 {
		t.Fatalf("records: %v, %v", records, err)
	}
	// OpenSSL pkey -pubout -outform DER for the Ed25519 seed 01 repeated
	// 32 times, hashed with SHA-256 independently of the implementation.
	want := "3 1 1 fd110d301d2f077de1414b8f99f441b1403fab207b2052fbd2c065e4ee8e7dc2"
	if got := records[0]; got.Type != "TLSA" || got.Name != "_25._tcp.mx.example.test" || got.Value != want || got.TTL != 3600 {
		t.Errorf("record = %+v", got)
	}
	// A PEM chain must hash the leaf, not the following issuer.
	chain := append(append([]byte{}, cert...), tlsaTestCert(t, 2, 2)...)
	withChain, err := SMTPTLSARecords("mx.example.test", chain)
	if err != nil || len(withChain) != 1 || withChain[0] != records[0] {
		t.Fatalf("chain did not use its leaf: %v, %v", withChain, err)
	}
}

func TestSMTPTLSARecordsRenewalAndRollover(t *testing.T) {
	current := tlsaTestCert(t, 1, 1)
	renewed := tlsaTestCert(t, 1, 2)
	next := tlsaTestCert(t, 2, 3)
	oldRecords, err := SMTPTLSARecords("mx.example.test", current)
	if err != nil {
		t.Fatal(err)
	}
	renewedRecords, err := SMTPTLSARecords("mx.example.test", renewed)
	if err != nil || len(renewedRecords) != 1 || renewedRecords[0] != oldRecords[0] {
		t.Fatalf("same-key renewal changed the record: %v, %v", renewedRecords, err)
	}
	staged, err := SMTPTLSARecords("mx.example.test", current, next, renewed)
	if err != nil || len(staged) != 2 || staged[0] != oldRecords[0] || staged[1].Value == staged[0].Value {
		t.Fatalf("rollover must retain the current key and add the new one once: %v, %v", staged, err)
	}
}

func TestSMTPTLSARecordsInvalidInputs(t *testing.T) {
	cert := tlsaTestCert(t, 1, 1)
	for _, host := range []string{"", "192.0.2.1", "mx.example.test\n", strings.Repeat("a.", 125) + "test"} {
		if _, err := SMTPTLSARecords(host, cert); err == nil {
			t.Errorf("accepted invalid or too-long TLSA owner %q", host)
		}
	}
	for _, certs := range [][][]byte{nil, {[]byte("not PEM")}, {pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("not a certificate")})}, {pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("not DER")})}, {cert, []byte("bad staged certificate")}} {
		if _, err := SMTPTLSARecords("mx.example.test", certs...); err == nil {
			t.Error("accepted a missing or invalid current/staged certificate")
		}
	}
	records, err := SMTPTLSARecords("mäl.example", cert)
	if err != nil || len(records) != 1 || !strings.HasPrefix(records[0].Name, "_25._tcp.xn--") {
		t.Fatalf("IDN owner: %v, %v", records, err)
	}
}
