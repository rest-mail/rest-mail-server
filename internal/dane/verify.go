package dane

import (
	"bytes"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"errors"
	"fmt"
	"time"
)

// TLSA usages SMTP can use (RFC 7672 §3.1). PKIX-TA(0) and PKIX-EE(1) are unusable for
// SMTP (§3.1.3): a sender treats them as if they were not published.
const (
	UsageDANETA = 2
	UsageDANEEE = 3
)

// Usable keeps the records RFC 7672 lets an SMTP client act on: DANE-TA and DANE-EE,
// with a selector and matching type it knows.
func Usable(records []TLSA) []TLSA {
	var out []TLSA
	for _, r := range records {
		if (r.Usage == UsageDANETA || r.Usage == UsageDANEEE) && r.Selector <= 1 && r.MatchingType <= 2 {
			out = append(out, r)
		}
	}
	return out
}

// ErrNoMatch is returned when no usable TLSA record matches the server's chain.
var ErrNoMatch = errors.New("dane: no TLSA record matches the server's certificate")

// Verify checks the certificate chain an SMTP server presented (leaf first) against its
// usable TLSA records, as RFC 7672 §3 describes:
//
//   - DANE-EE(3): the leaf certificate or its public key matches the record. Names and
//     validity dates are not checked (§3.1.1): the DNSSEC-signed record is the trust.
//   - DANE-TA(2): a certificate in the chain matches the record and is used as the only
//     trust anchor for an ordinary chain check of the leaf, including its validity and a
//     name match against host (§3.1.2, §3.2.2). A trust anchor whose record holds only
//     a key (selector 1) must be presented as a certificate in the chain.
//
// Any one matching record is enough.
func Verify(records []TLSA, host string, chain []*x509.Certificate, now time.Time) error {
	usable := Usable(records)
	if len(usable) == 0 {
		return errors.New("dane: no usable TLSA records")
	}
	if len(chain) == 0 {
		return errors.New("dane: the server presented no certificate")
	}
	var reasons []error
	for _, r := range usable {
		switch r.Usage {
		case UsageDANEEE:
			if matches(r, chain[0]) {
				return nil
			}
		case UsageDANETA:
			for i, anchor := range chain {
				if !matches(r, anchor) {
					continue
				}
				err := verifyFromAnchor(chain[:i+1], anchor, host, now)
				if err == nil {
					return nil
				}
				reasons = append(reasons, err)
			}
		}
	}
	if len(reasons) > 0 {
		return fmt.Errorf("%w: %v", ErrNoMatch, errors.Join(reasons...))
	}
	return ErrNoMatch
}

// verifyFromAnchor checks the leaf chains to anchor through the certificates presented
// between them, and names host.
func verifyFromAnchor(chain []*x509.Certificate, anchor *x509.Certificate, host string, now time.Time) error {
	leaf := chain[0]
	if leaf.Equal(anchor) {
		// A DANE-TA record matching the leaf itself is a self-signed trust anchor: the
		// name must still match (§3.2.2).
		if err := leaf.VerifyHostname(host); err != nil {
			return err
		}
		if now.Before(leaf.NotBefore) || now.After(leaf.NotAfter) {
			return errors.New("dane: the certificate is outside its validity period")
		}
		return nil
	}
	roots := x509.NewCertPool()
	roots.AddCert(anchor)
	intermediates := x509.NewCertPool()
	for _, c := range chain[1 : len(chain)-1] {
		intermediates.AddCert(c)
	}
	_, err := leaf.Verify(x509.VerifyOptions{
		DNSName:       host,
		Roots:         roots,
		Intermediates: intermediates,
		CurrentTime:   now,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	})
	return err
}

// matches reports whether a certificate matches one TLSA record's selector and
// matching type (RFC 6698 §2.1.2, §2.1.3).
func matches(r TLSA, cert *x509.Certificate) bool {
	var selected []byte
	switch r.Selector {
	case 0:
		selected = cert.Raw
	case 1:
		selected = cert.RawSubjectPublicKeyInfo
	default:
		return false
	}
	switch r.MatchingType {
	case 0:
		return bytes.Equal(selected, r.Data)
	case 1:
		sum := sha256.Sum256(selected)
		return bytes.Equal(sum[:], r.Data)
	case 2:
		sum := sha512.Sum512(selected)
		return bytes.Equal(sum[:], r.Data)
	}
	return false
}
