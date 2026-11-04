package handlers

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"strings"

	"github.com/rest-mail/rest-mail-server/internal/mail"
)

// errKeyMismatch is a private key that is not the certificate's own: the pair
// parses, but no client can complete a handshake with it (issue #1704).
var errKeyMismatch = errors.New("the private key does not belong to this certificate")

// certificateKeyPair loads a certificate and its private key exactly as the
// gateways do when they serve it (tls.X509KeyPair), so a pair accepted here is
// one they can load.
func certificateKeyPair(certPEM, keyPEM string) (tls.Certificate, error) {
	pair, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err == nil {
		return pair, nil
	}
	if strings.Contains(err.Error(), "does not match public key") {
		return tls.Certificate{}, errKeyMismatch
	}
	return tls.Certificate{}, fmt.Errorf("the private key could not be read: %w", err)
}

// certificateName is the name a domain's certificate must carry: the name a
// client sends as SNI to reach it, which the gateways look the certificate up
// by. SNI only carries A-labels (RFC 6066), so an internationalized domain is
// matched by its A-label.
func certificateName(domain string) string {
	ascii, err := mail.DomainASCII(strings.TrimSuffix(domain, "."))
	if err != nil {
		return strings.ToLower(domain)
	}
	return strings.ToLower(ascii)
}

// certificateCovers says whether a client asking for name would accept leaf for
// it. Names come from the subject alternative names, wildcards included; the
// common name counts only when there are none (RFC 6125 section 6.4.4).
func certificateCovers(leaf *x509.Certificate, name string) bool {
	if leaf.VerifyHostname(name) == nil {
		return true
	}
	if len(leaf.DNSNames) > 0 || len(leaf.IPAddresses) > 0 || leaf.Subject.CommonName == "" {
		return false
	}
	byCommonName := *leaf
	byCommonName.DNSNames = []string{leaf.Subject.CommonName}
	return byCommonName.VerifyHostname(name) == nil
}

// certificateServes says why a stored certificate and key cannot serve domain,
// or nil when the gateways can load the pair and a client asking for the
// domain's name would accept it.
func certificateServes(certPEM, keyPEM, domain string) error {
	pair, err := certificateKeyPair(certPEM, keyPEM)
	if err != nil {
		return err
	}
	leaf := pair.Leaf
	if leaf == nil {
		if leaf, err = x509.ParseCertificate(pair.Certificate[0]); err != nil {
			return fmt.Errorf("the certificate could not be read: %w", err)
		}
	}
	if name := certificateName(domain); !certificateCovers(leaf, name) {
		return fmt.Errorf("the certificate does not name %s", name)
	}
	return nil
}
