package tlsutil

import (
	"crypto/tls"
	"errors"
	"fmt"
	"testing"
)

// The gateways ask more than one loader for a certificate. A loader that simply
// does not know the name must hand on to the next one, but a loader that refuses
// because a domain served here has no usable certificate must end the handshake:
// letting the next loader answer is how a client ended up with another name's
// certificate (issue #290).
func TestChain(t *testing.T) {
	cert, _, _ := selfSigned(t, "served.test")
	hello := &tls.ClientHelloInfo{ServerName: "served.test"}

	notOurs := func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
		return nil, fmt.Errorf("%w: served.test", ErrNotServedHere)
	}
	broken := func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
		return nil, errors.New("no usable certificate for served.test, which is served here")
	}
	serves := func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
		return &cert, nil
	}

	t.Run("a name the first loader does not know goes to the next", func(t *testing.T) {
		got, err := Chain(notOurs, serves)(hello)
		if err != nil {
			t.Fatalf("refused: %v", err)
		}
		if got != &cert {
			t.Error("got a different certificate than the second loader returned")
		}
	})

	t.Run("a domain served here but unusable stops the chain", func(t *testing.T) {
		got, err := Chain(broken, serves)(hello)
		if err == nil {
			t.Fatal("the chain answered after a loader refused a domain served here")
		}
		if got != nil {
			t.Errorf("cert = %v, want nil", got)
		}
		if errors.Is(err, ErrNotServedHere) {
			t.Error("the refusal was reported as an unknown name")
		}
	})

	t.Run("no loader knows the name", func(t *testing.T) {
		_, err := Chain(notOurs, notOurs)(hello)
		if !errors.Is(err, ErrNotServedHere) {
			t.Fatalf("err = %v, want it to report a name that is not served here", err)
		}
	})

	t.Run("nil loaders are skipped", func(t *testing.T) {
		got, err := Chain(nil, serves)(hello)
		if err != nil || got != &cert {
			t.Fatalf("cert = %v, err = %v, want the second loader's certificate", got, err)
		}
	})
}
