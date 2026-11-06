package imap

import "crypto/tls"

// ServerTLSConfig is the TLS configuration the IMAP gateway serves on its
// implicit-TLS listener (993), with getCertificate choosing the certificate for
// each handshake.
//
// RFC 9051 §11.1 requires TLS 1.2 or newer and the
// TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256 suite, and defers to RFC 9325 for the
// rest of the profile. The TLS 1.2 suites are the ECDHE AEAD ones RFC 9325 §4.2
// recommends, so no static-RSA key exchange and no CBC suite is ever
// negotiated; TLS 1.3 suites are not configurable in Go and are all AEAD.
func ServerTLSConfig(getCertificate func(*tls.ClientHelloInfo) (*tls.Certificate, error)) *tls.Config {
	return &tls.Config{
		MinVersion:     tls.VersionTLS12,
		CipherSuites:   ServerCipherSuites(),
		GetCertificate: getCertificate,
	}
}

// ServerCipherSuites lists the TLS 1.2 cipher suites ServerTLSConfig allows.
func ServerCipherSuites() []uint16 {
	return []uint16{
		tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
		tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
		tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
		tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
		tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256,
		tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256,
	}
}
