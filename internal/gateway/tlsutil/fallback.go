package tlsutil

import (
	"crypto/tls"
	"crypto/x509"
)

// certCovers reports whether cert is valid for the host name a client asked for.
//
// The rule these loaders keep is "never answer under a name the certificate does
// not carry" (issue #290), which is not the same as "never answer with the
// server's own certificate". Most installations have a single keypair whose SANs
// list every host name they serve — the testbed issues exactly that — and for
// those names it is the right answer. For anything else it is another name's
// certificate, and the handshake is refused instead.
func certCovers(cert *tls.Certificate, name string) bool {
	if cert == nil || name == "" {
		return false
	}
	leaf := cert.Leaf
	if leaf == nil {
		if len(cert.Certificate) == 0 {
			return false
		}
		parsed, err := x509.ParseCertificate(cert.Certificate[0])
		if err != nil {
			return false
		}
		leaf = parsed
	}
	return leaf.VerifyHostname(name) == nil
}
