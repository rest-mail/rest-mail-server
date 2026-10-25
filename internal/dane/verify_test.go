package dane

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"testing"
	"time"
)

var testNow = time.Date(2026, 10, 25, 4, 0, 0, 0, time.UTC)

type pair struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

// issue makes a certificate for names, signed by parent (self-signed when parent is nil).
func issue(t *testing.T, cn string, names []string, isCA bool, parent *pair, notAfter time.Time) pair {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: cn},
		DNSNames:              names,
		NotBefore:             testNow.Add(-time.Hour),
		NotAfter:              notAfter,
		IsCA:                  isCA,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	signer, signerCert := key, tmpl
	if parent != nil {
		signer, signerCert = parent.key, parent.cert
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, signerCert, &key.PublicKey, signer)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return pair{cert, key}
}

func record(usage, selector, matching uint8, cert *x509.Certificate) TLSA {
	data := cert.Raw
	if selector == 1 {
		data = cert.RawSubjectPublicKeyInfo
	}
	switch matching {
	case 1:
		sum := sha256.Sum256(data)
		data = sum[:]
	case 2:
		sum := sha512.Sum512(data)
		data = sum[:]
	}
	return TLSA{Usage: usage, Selector: selector, MatchingType: matching, Data: data}
}

func TestDANEEEMatchesTheLeafAloneIgnoringNamesAndDates(t *testing.T) {
	// Expired, for another name, self-signed: DANE-EE trusts the record, not the certificate.
	leaf := issue(t, "anything", []string{"other.example"}, false, nil, testNow.Add(-time.Minute))
	for _, sel := range []uint8{0, 1} {
		for _, mt := range []uint8{0, 1, 2} {
			if err := Verify([]TLSA{record(3, sel, mt, leaf.cert)}, "mx.example.test", []*x509.Certificate{leaf.cert}, testNow); err != nil {
				t.Errorf("3 %d %d: %v", sel, mt, err)
			}
		}
	}
	other := issue(t, "x", nil, false, nil, testNow.Add(time.Hour))
	if err := Verify([]TLSA{record(3, 1, 1, other.cert)}, "mx.example.test", []*x509.Certificate{leaf.cert}, testNow); !errors.Is(err, ErrNoMatch) {
		t.Fatalf("a record for another key matched: %v", err)
	}
}

func TestDANETAChainsTheLeafToTheMatchingAnchor(t *testing.T) {
	ca := issue(t, "Example CA", nil, true, nil, testNow.Add(24*time.Hour))
	inter := issue(t, "Example Intermediate", nil, true, &ca, testNow.Add(24*time.Hour))
	leaf := issue(t, "mx", []string{"mx.example.test"}, false, &inter, testNow.Add(time.Hour))
	chain := []*x509.Certificate{leaf.cert, inter.cert, ca.cert}

	for name, r := range map[string]TLSA{"root": record(2, 0, 1, ca.cert), "intermediate": record(2, 1, 1, inter.cert)} {
		if err := Verify([]TLSA{r}, "mx.example.test", chain, testNow); err != nil {
			t.Errorf("anchor %s: %v", name, err)
		}
	}
	// DANE-TA checks the name and dates, unlike DANE-EE.
	if err := Verify([]TLSA{record(2, 0, 1, ca.cert)}, "mx2.example.test", chain, testNow); err == nil {
		t.Error("DANE-TA accepted a leaf for another name")
	}
	if err := Verify([]TLSA{record(2, 0, 1, ca.cert)}, "mx.example.test", chain, testNow.Add(2*time.Hour)); err == nil {
		t.Error("DANE-TA accepted an expired leaf")
	}
	// An anchor the server did not present cannot be used: DANE-TA requires it in the chain.
	if err := Verify([]TLSA{record(2, 0, 1, ca.cert)}, "mx.example.test", []*x509.Certificate{leaf.cert, inter.cert}, testNow); !errors.Is(err, ErrNoMatch) {
		t.Errorf("an absent anchor matched: %v", err)
	}
	// A record matching a certificate that did not issue the leaf proves nothing.
	stranger := issue(t, "Stranger CA", nil, true, nil, testNow.Add(24*time.Hour))
	if err := Verify([]TLSA{record(2, 0, 1, stranger.cert)}, "mx.example.test", []*x509.Certificate{leaf.cert, stranger.cert}, testNow); err == nil {
		t.Error("an anchor that did not issue the leaf was accepted")
	}
}

func TestPKIXUsagesAreUnusableForSMTP(t *testing.T) {
	leaf := issue(t, "mx", []string{"mx.example.test"}, false, nil, testNow.Add(time.Hour))
	records := []TLSA{record(0, 0, 1, leaf.cert), record(1, 1, 1, leaf.cert)}
	if len(Usable(records)) != 0 {
		t.Fatal("PKIX-TA and PKIX-EE are unusable for SMTP (RFC 7672 §3.1.3)")
	}
	if err := Verify(records, "mx.example.test", []*x509.Certificate{leaf.cert}, testNow); err == nil {
		t.Fatal("only unusable records, yet verified")
	}
}

func TestAnyOneUsableRecordIsEnough(t *testing.T) {
	leaf := issue(t, "mx", []string{"mx.example.test"}, false, nil, testNow.Add(time.Hour))
	old := issue(t, "old", nil, false, nil, testNow.Add(time.Hour))
	// During a key rollover both the old and the new key are published.
	records := []TLSA{record(3, 1, 1, old.cert), record(0, 0, 1, leaf.cert), record(3, 1, 1, leaf.cert)}
	if err := Verify(records, "mx.example.test", []*x509.Certificate{leaf.cert}, testNow); err != nil {
		t.Fatal(err)
	}
	if err := Verify(records, "mx.example.test", nil, testNow); err == nil {
		t.Fatal("no certificate, yet verified")
	}
}
