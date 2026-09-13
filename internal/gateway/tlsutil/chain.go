package tlsutil

import (
	"crypto/tls"
	"errors"
	"fmt"
)

// GetCertificateFunc is the tls.Config.GetCertificate callback.
type GetCertificateFunc = func(*tls.ClientHelloInfo) (*tls.Certificate, error)

// Chain tries each loader in turn.
//
// A loader that refuses with ErrNotServedHere does not know the name, so the next
// loader gets a turn. Any other refusal ends the handshake there and then: it
// means the name IS served here and its certificate is missing or unusable, and
// no later loader may answer that client under a different name (issue #290).
// When every loader says the name is not served here, that refusal is returned.
func Chain(loaders ...GetCertificateFunc) GetCertificateFunc {
	return func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
		var notOurs error
		for _, load := range loaders {
			if load == nil {
				continue
			}
			cert, err := load(hello)
			if err == nil && cert != nil {
				return cert, nil
			}
			if err != nil && !errors.Is(err, ErrNotServedHere) {
				return nil, err
			}
			if err != nil {
				notOurs = err
			}
		}
		if notOurs != nil {
			return nil, notOurs
		}
		return nil, fmt.Errorf("%w: %s", ErrNotServedHere, hello.ServerName)
	}
}
