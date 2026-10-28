package tlsutil

import (
	"crypto/tls"
	"fmt"
	"strings"
	"time"

	"github.com/restmail/restmail/internal/tlsfiles"
)

// FileCertLoader reloads the primary serving pair when either file is replaced.
// It retains the last complete pair while a writer publishes the key and cert
// with separate renames. Handshakes poll file identity at most every five seconds
// so a missed notification cannot leave a gateway serving an old pair indefinitely.
type FileCertLoader struct {
	*tlsfiles.Loader
}

func NewFileCertLoader(certPath, keyPath string) (*FileCertLoader, error) {
	return newFileCertLoader(certPath, keyPath, tlsfiles.CheckInterval)
}

func newFileCertLoader(certPath, keyPath string, interval time.Duration) (*FileCertLoader, error) {
	loader, err := tlsfiles.New(certPath, keyPath, interval)
	return &FileCertLoader{Loader: loader}, err
}

// GetCertificate serves the primary pair for connections without SNI and for
// names it covers. It refuses to offer that pair for a different hostname.
func (l *FileCertLoader) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	cert := l.Current()
	name := strings.ToLower(hello.ServerName)
	if name == "" || certCovers(cert, name) {
		return cert, nil
	}
	return nil, fmt.Errorf("%w: %s", ErrNotServedHere, name)
}
