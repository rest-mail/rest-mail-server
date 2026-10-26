package queue

import (
	"context"
	"errors"
	"net"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/rest-mail/go-mtasts/pkg/mtasts"
)

// A message that carries "TLS-Required: No" applies no MTA-STS policy, and
// its policy is not looked up; it reaches an MX whose certificate an enforce
// policy refuses, over STARTTLS still. Without the field the same delivery
// is refused (RFC 8689 §4.2.2, #724).
func TestTLSRequiredNo_IgnoresTheRecipientsPolicy(t *testing.T) {
	cert, _ := relayTestCertificate(t, false) // nothing vouches for it
	lookups := 0
	w := &Worker{hostname: "sender.test", allowPrivateDest: true, sendDeadline: defaultSendDeadline, stsEnforce: true}
	w.sts = mtasts.NewResolver()
	w.sts.LookupTXT = func(context.Context, string) ([]string, error) {
		lookups++
		return []string{"v=STSv1; id=1"}, nil
	}
	w.sts.FetchPolicy = func(context.Context, string) ([]byte, error) {
		return []byte("version: STSv1\nmode: enforce\nmx: 127.0.0.1\nmax_age: 86400\n"), nil
	}

	deliver := func(raw string) ([]string, error) {
		item := tlsTestItem()
		item.RawMessage = raw
		policy, stsErr := w.recipientPolicy(item, item.Domain)
		settings, finish := relayTestPeer(t, cert, relayPeerOptions{noAUTH: true})
		err := w.deliverSMTP(net.JoinHostPort(settings.host, strconv.Itoa(settings.port)), settings.host, item, policy, stsErr)
		return finish().commands, err
	}

	_, err := deliver("Subject: enforced\r\n\r\nhello\r\n")
	var enforced *mtasts.EnforceError
	if !errors.As(err, &enforced) || lookups != 1 {
		t.Fatalf("without TLS-Required: %v after %d lookups, want an enforce refusal", err, lookups)
	}

	commands, err := deliver("Subject: certificate expired\r\nTLS-Required: No\r\n\r\nhello\r\n")
	if err != nil {
		t.Fatalf("TLS-Required: No: %v", err)
	}
	if lookups != 1 {
		t.Errorf("the policy was looked up for a TLS-Required: No message")
	}
	if !slices.Contains(commands, "STARTTLS") || !slices.ContainsFunc(commands, func(c string) bool { return strings.HasPrefix(c, "MAIL") }) {
		t.Errorf("commands %v, want STARTTLS and the message", commands)
	}
}
