package queue

import (
	"bytes"
	"errors"
	"net"
	"strconv"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/dane"
)

// A handshake that fails DANE verification keeps dane.ErrNoMatch in its error chain,
// which is how the session is reported as tlsa-invalid rather than validation-failure.
func TestDANEHandshakeFailureKeepsErrNoMatch(t *testing.T) {
	cert, _ := relayTestCertificate(t, false)
	settings, finish := relayTestPeer(t, cert, relayPeerOptions{noAUTH: true})
	wrong := []dane.TLSA{{Usage: dane.UsageDANEEE, Selector: 1, MatchingType: 1, Data: bytes.Repeat([]byte{7}, 32)}}
	link, err := daneTestWorker().openSMTP(net.JoinHostPort(settings.host, strconv.Itoa(settings.port)), settings.host, true, false, wrong)
	if err != nil {
		t.Fatal(err)
	}
	_ = link.client.Close()
	finish()
	if !errors.Is(link.tlsErr, dane.ErrNoMatch) {
		t.Fatalf("handshake error %v does not wrap dane.ErrNoMatch", link.tlsErr)
	}
}
