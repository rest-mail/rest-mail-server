package queue

import (
	"errors"
	"strings"
	"testing"

	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

const relayRouteMasterKey = "relay-route-master-key"

func openRelayRouteDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := openQueueTestDB(t)
	if err := db.AutoMigrate(&models.DomainRelay{}); err != nil {
		t.Fatalf("migrate domain_relays: %v", err)
	}
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })
	return tx
}

func seedRelay(t *testing.T, tx *gorm.DB, domainName string, rel models.DomainRelay) {
	t.Helper()
	var dom models.Domain
	if err := tx.Where("name = ?", domainName).First(&dom).Error; err != nil {
		t.Fatalf("load domain: %v", err)
	}
	rel.DomainID = dom.ID
	if err := tx.Create(&rel).Error; err != nil {
		t.Fatalf("seed relay: %v", err)
	}
}

// TestRelayRouteDirectWhenUnset: a domain with no relay row, or one set to
// direct, keeps the direct MX path.
func TestRelayRouteDirectWhenUnset(t *testing.T) {
	tx := openRelayRouteDB(t)
	domainName := seedSignedDomain(t, tx, relayRouteMasterKey)
	w := &Worker{db: tx, masterKey: relayRouteMasterKey}
	item := models.OutboundQueue{Sender: "alice@" + domainName, Recipient: "bob@example.net"}

	if got, err := w.relayRoute(item); err != nil || got != nil {
		t.Fatalf("no relay row: got %+v, %v; want direct", got, err)
	}
	seedRelay(t, tx, domainName, models.DomainRelay{Transport: models.OutboundTransportDirect})
	if got, err := w.relayRoute(item); err != nil || got != nil {
		t.Fatalf("transport direct: got %+v, %v; want direct", got, err)
	}
}

// TestRelayRouteFailsClosedOnUndecryptablePassword: a password that the
// worker's MASTER_KEY cannot read is an error (retry), never a direct send or
// an unauthenticated relay attempt.
func TestRelayRouteFailsClosedOnUndecryptablePassword(t *testing.T) {
	tx := openRelayRouteDB(t)
	domainName := seedSignedDomain(t, tx, relayRouteMasterKey)
	enc, err := models.EncryptRelayPassword(relayTestPassword, "another-master-key")
	if err != nil {
		t.Fatal(err)
	}
	seedRelay(t, tx, domainName, models.DomainRelay{
		Transport: models.OutboundTransportRelay, Host: "relay.example.test", Port: 587,
		TLSMode: models.RelayTLSStartTLS, Username: relayTestUser, PasswordEnc: enc,
	})
	w := &Worker{db: tx, masterKey: relayRouteMasterKey, hostname: "sender.test"}
	item := models.OutboundQueue{Sender: "alice@" + domainName, Recipient: "bob@example.net", RawMessage: "Subject: x\r\n\r\nx\r\n"}

	got, err := w.relayRoute(item)
	if !errors.Is(err, models.ErrRelayPasswordUndecryptable) || got != nil {
		t.Fatalf("got %+v, %v; want ErrRelayPasswordUndecryptable", got, err)
	}
	deliverErr := w.deliver(item)
	if !errors.Is(deliverErr, models.ErrRelayPasswordUndecryptable) {
		t.Fatalf("deliver: %v; want a retryable credentials error", deliverErr)
	}
	if strings.Contains(deliverErr.Error(), relayTestPassword) {
		t.Fatal("error carries the password")
	}
}

// TestDeliverRoutesThroughConfiguredRelay: with transport relay, deliver
// prepares the message once (Bcc stripped, DKIM-signed with the sender
// domain's key) and submits it to the relay over TLS with the decrypted
// credentials, in both TLS modes.
func TestDeliverRoutesThroughConfiguredRelay(t *testing.T) {
	cert, roots := relayTestCertificate(t, false)
	for _, mode := range []string{models.RelayTLSStartTLS, models.RelayTLSImplicit} {
		t.Run(mode, func(t *testing.T) {
			tx := openRelayRouteDB(t)
			domainName := seedSignedDomain(t, tx, relayRouteMasterKey)
			peer, finish := relayTestPeer(t, cert, relayPeerOptions{implicit: mode == models.RelayTLSImplicit, requireAUTH: true})
			enc, err := models.EncryptRelayPassword(relayTestPassword, relayRouteMasterKey)
			if err != nil {
				t.Fatal(err)
			}
			seedRelay(t, tx, domainName, models.DomainRelay{
				Transport: models.OutboundTransportRelay, Host: peer.host, Port: peer.port,
				TLSMode: mode, Username: relayTestUser, PasswordEnc: enc,
			})
			w := &Worker{db: tx, masterKey: relayRouteMasterKey, hostname: "sender.test", sendDeadline: defaultSendDeadline, relayRoots: roots}
			item := models.OutboundQueue{
				Sender:    "alice@" + domainName,
				Recipient: "bob@example.net",
				// The recipient domain cannot resolve: reaching the relay proves
				// no MX lookup or RESTMAIL probe ran.
				Domain: "example.invalid",
				RawMessage: "From: alice@" + domainName + "\r\n" +
					"To: bob@example.net\r\n" +
					"Bcc: hidden@example.net\r\n" +
					"Subject: relayed\r\n" +
					"\r\n" +
					"hello\r\n",
			}

			if err := w.deliver(item); err != nil {
				t.Fatalf("deliver: %v", err)
			}
			result := finish()
			if result.auth != "\x00"+relayTestUser+"\x00"+relayTestPassword {
				t.Fatal("relay did not receive the decrypted credentials")
			}
			if result.authBeforeTLS {
				t.Fatal("AUTH before TLS")
			}
			if !strings.Contains(result.data, "DKIM-Signature:") || !strings.Contains(result.data, "d="+domainName) {
				t.Fatalf("relayed message not DKIM-signed for %s:\n%s", domainName, result.data)
			}
			if strings.Contains(result.data, "hidden@example.net") {
				t.Fatalf("Bcc reached the relay:\n%s", result.data)
			}
			if strings.Count(result.data, "DKIM-Signature:") != 1 {
				t.Fatalf("message prepared more than once:\n%s", result.data)
			}
		})
	}
}
