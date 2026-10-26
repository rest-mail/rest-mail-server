package queue

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"testing"

	"github.com/rest-mail/go-mtasts/pkg/mtasts"
	"github.com/restmail/restmail/internal/db/models"
)

// requireTLSWorker is a worker whose recipient domain example.test publishes policy, or none
// when policy is "".
func requireTLSWorker(policy string) *Worker {
	w := &Worker{hostname: "sender.test", allowPrivateDest: true, sendDeadline: defaultSendDeadline, stsEnforce: true}
	w.sts = mtasts.NewResolver()
	w.sts.LookupTXT = func(context.Context, string) ([]string, error) {
		if policy == "" {
			return nil, &net.DNSError{Err: "no such host", IsNotFound: true}
		}
		return []string{"v=STSv1; id=1"}, nil
	}
	w.sts.FetchPolicy = func(context.Context, string) ([]byte, error) { return []byte(policy), nil }
	return w
}

// mailCommand is the MAIL command among commands, "" when none was sent.
func mailCommand(commands []string) string {
	for _, c := range commands {
		if strings.HasPrefix(strings.ToUpper(c), "MAIL FROM:") {
			return c
		}
	}
	return ""
}

// A message whose sender required TLS goes only to an MX the domain's MTA-STS policy names,
// over TLS with a certificate verified for it, which offers REQUIRETLS, and MAIL FROM carries
// REQUIRETLS on (RFC 8689 §4.2.1, #724). Each requirement that fails refuses the host: 5.7.30
// when the MX lacks REQUIRETLS, 5.7.10 when TLS cannot be had, or the MX is not one the policy
// names, and the message is not sent.
func TestRequireTLS_Delivery(t *testing.T) {
	const enforce = "version: STSv1\nmode: enforce\nmx: 127.0.0.1\nmax_age: 86400\n"
	const testingMode = "version: STSv1\nmode: testing\nmx: 127.0.0.1\nmax_age: 86400\n"
	const elsewhere = "version: STSv1\nmode: enforce\nmx: mx.example.test\nmax_age: 86400\n"
	cert, roots := relayTestCertificate(t, false)

	deliver := func(t *testing.T, policy string, trusted bool, peer relayPeerOptions, edit func(*models.OutboundQueue)) ([]string, error) {
		t.Helper()
		w := requireTLSWorker(policy)
		if trusted {
			w.smtpRoots = roots
		}
		item := tlsTestItem()
		item.RequireTLS = true
		if edit != nil {
			edit(&item)
		}
		stsPolicy, stsErr := w.recipientPolicy(item, item.Domain)
		peer.noAUTH = true
		settings, finish := relayTestPeer(t, cert, peer)
		err := w.deliverSMTP(net.JoinHostPort(settings.host, strconv.Itoa(settings.port)), settings.host, item, stsPolicy, stsErr)
		return finish().commands, err
	}
	refusal := func(t *testing.T, err error, enhanced string) {
		t.Helper()
		var se *SMTPError
		if !errors.As(err, &se) || se.Code != 550 || se.Enhanced != enhanced {
			t.Fatalf("err %v, want a 550 %s refusal", err, enhanced)
		}
	}

	t.Run("delivered with REQUIRETLS", func(t *testing.T) {
		for _, policy := range []string{enforce, testingMode} {
			commands, err := deliver(t, policy, true, relayPeerOptions{requireTLS: true}, nil)
			if err != nil {
				t.Fatalf("policy %q: %v", policy, err)
			}
			if mail := mailCommand(commands); !strings.HasSuffix(mail, " REQUIRETLS") || !strings.HasPrefix(mail, "MAIL FROM:<sender@example.test>") {
				t.Fatalf("MAIL command %q, want it to carry REQUIRETLS", mail)
			}
		}
	})
	t.Run("the MX does not offer REQUIRETLS", func(t *testing.T) {
		commands, err := deliver(t, enforce, true, relayPeerOptions{}, nil)
		refusal(t, err, "5.7.30")
		if mail := mailCommand(commands); mail != "" {
			t.Fatalf("sent %q to an MX without REQUIRETLS", mail)
		}
	})
	t.Run("the certificate does not verify", func(t *testing.T) {
		commands, err := deliver(t, enforce, false, relayPeerOptions{requireTLS: true}, nil)
		refusal(t, err, "5.7.10")
		if mail := mailCommand(commands); mail != "" {
			t.Fatalf("sent %q over an unverified session", mail)
		}
	})
	t.Run("the header TLS-Required: No is ignored", func(t *testing.T) {
		_, err := deliver(t, enforce, false, relayPeerOptions{requireTLS: true}, func(item *models.OutboundQueue) {
			item.RawMessage = "TLS-Required: No\r\nSubject: both\r\n\r\nhello\r\n"
		})
		refusal(t, err, "5.7.10")
	})
	t.Run("a bounce is not held to it", func(t *testing.T) {
		// A null reverse path goes even where REQUIRETLS is not offered (§5).
		commands, err := deliver(t, enforce, true, relayPeerOptions{}, func(item *models.OutboundQueue) { item.Sender = "" })
		if err != nil {
			t.Fatalf("a bounce: %v", err)
		}
		if mail := mailCommand(commands); mail == "" || strings.Contains(mail, "REQUIRETLS") {
			t.Fatalf("a bounce's MAIL command %q, want one without REQUIRETLS", mail)
		}
	})

	// Without a policy naming the MX nothing is dialed: there is no MX to prove.
	for name, policy := range map[string]string{"no MTA-STS policy": "", "an MX the policy does not name": elsewhere} {
		t.Run(name, func(t *testing.T) {
			w := requireTLSWorker(policy)
			w.smtpRoots = roots
			item := tlsTestItem()
			item.RequireTLS = true
			stsPolicy, stsErr := w.recipientPolicy(item, item.Domain)
			err := w.deliverSMTP("127.0.0.1:1", "127.0.0.1", item, stsPolicy, stsErr)
			refusal(t, err, "5.7.10")
		})
	}
}
