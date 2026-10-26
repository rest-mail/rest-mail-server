package queue

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"strings"
	"testing"

	arc "github.com/rest-mail/go-arc/pkg/arc"
	dkim "github.com/rest-mail/go-dkim/pkg/dkim"
	"github.com/restmail/restmail/internal/db/models"
)

// A message a Sieve redirect forwards leaves DKIM-signed and then ARC-sealed by the
// forwarding domain, the seal last, recording what this server found when it accepted the
// message, and the chain validates against the domain's published key; mail that is not a
// forward is not sealed (RFC 8617 §5.1, #731).
func TestPrepareOutbound_SealsAForward(t *testing.T) {
	tx := openRelayRouteDB(t)
	forwarder := seedSignedDomain(t, tx, relayRouteMasterKey)
	var dom models.Domain
	if err := tx.Where("name = ?", forwarder).First(&dom).Error; err != nil {
		t.Fatal(err)
	}
	keyPEM, err := models.LoadDKIMPrivateKey(dom.DKIMPrivateKey, relayRouteMasterKey)
	if err != nil {
		t.Fatal(err)
	}
	key, err := dkim.ParsePrivateKey(keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	record := "v=DKIM1; k=rsa; p=" + base64.StdEncoding.EncodeToString(der)
	resolver := func(_ context.Context, name string) ([]string, error) {
		if strings.TrimSuffix(name, ".") == "sel._domainkey."+forwarder {
			return []string{record}, nil
		}
		return nil, nil
	}

	results := "restmail; spf=pass smtp.mailfrom=remote.test; dkim=pass header.d=remote.test; arc=none; dmarc=pass header.from=remote.test"
	original := "Delivered-To: fwd@elsewhere.test\r\n" +
		"From: alice@remote.test\r\nTo: dana@" + forwarder + "\r\nSubject: Lunch?\r\n" +
		"Date: Sat, 24 Oct 2026 05:00:00 +0000\r\nMessage-ID: <m1@remote.test>\r\n\r\nAre you free?\r\n"
	forward := models.OutboundQueue{
		Sender: "alice@remote.test", SendingDomain: forwarder, Recipient: "fwd@elsewhere.test",
		Domain: "elsewhere.test", RawMessage: original, ARCResults: results, ARCChain: "none",
	}
	w := &Worker{db: tx, masterKey: relayRouteMasterKey}

	sent, err := w.prepareOutbound(forward)
	if err != nil {
		t.Fatal(err)
	}
	seal, sig, dkimSig := strings.Index(sent, "ARC-Seal: i=1;"), strings.Index(sent, "ARC-Message-Signature: i=1;"), strings.Index(sent, "DKIM-Signature:")
	if seal != 0 || sig < 0 || dkimSig < sig {
		t.Fatalf("want the ARC set on top of the forwarding domain's DKIM signature:\n%s", sent)
	}
	for _, want := range []string{"ARC-Authentication-Results: i=1; " + results + "\r\n", "cv=none", "d=" + forwarder} {
		if !strings.Contains(sent, want) {
			t.Errorf("the forward lacks %q:\n%s", want, sent)
		}
	}
	if cv, reason := arc.Verify(context.Background(), []byte(sent), resolver); cv != "pass" {
		t.Fatalf("the forward's chain does not validate: %s (%s)", cv, reason)
	}

	// The same domain's own mail is DKIM-signed and not sealed.
	own := models.OutboundQueue{Sender: "dana@" + forwarder, Recipient: "bob@elsewhere.test", Domain: "elsewhere.test", RawMessage: original}
	plain, err := w.prepareOutbound(own)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plain, "ARC-") || !strings.HasPrefix(plain, "DKIM-Signature:") {
		t.Errorf("mail that is not a forward was sealed, or not signed:\n%s", plain)
	}
}
