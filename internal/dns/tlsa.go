package dns

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"net"
	"strings"

	"github.com/restmail/restmail/internal/mail"
)

// SMTPTLSARecords produces DANE-EE(3), SPKI(1), SHA-256(1) records for the
// current certificate and optionally a staged certificate (RFC 7672 §4).
// It prepares publication data; only a DNSSEC-authenticated TLSA RRset can
// establish DANE policy. No DNS or certificate deployment is changed here.
func SMTPTLSARecords(mailHost string, certificates ...[]byte) ([]DNSRecord, error) {
	host, err := mail.DomainASCII(strings.TrimSuffix(mailHost, "."))
	if err != nil || net.ParseIP(host) != nil || len(certificates) == 0 {
		return nil, fmt.Errorf("give an MX hostname and at least one PEM certificate")
	}
	owner := "_25._tcp." + host
	// The service labels must fit in DNS's 255-octet wire-name limit too.
	if len(owner) > 253 {
		return nil, fmt.Errorf("the TLSA owner name is too long")
	}
	seen := map[string]bool{}
	records := make([]DNSRecord, 0, len(certificates))
	for i, certPEM := range certificates {
		block, _ := pem.Decode(certPEM)
		if block == nil || block.Type != "CERTIFICATE" {
			return nil, fmt.Errorf("certificate %d must start with a PEM CERTIFICATE block", i+1)
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("certificate %d is not a valid X.509 certificate", i+1)
		}
		// Hash the complete DER SubjectPublicKeyInfo, not the leaf certificate
		// or the algorithm-specific public-key bytes. A renewal using the same
		// key therefore has exactly the same association data.
		digest := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
		value := "3 1 1 " + hex.EncodeToString(digest[:])
		if !seen[value] {
			records = append(records, DNSRecord{Type: "TLSA", Name: owner, Value: value, TTL: 3600})
			seen[value] = true
		}
	}
	return records, nil
}
