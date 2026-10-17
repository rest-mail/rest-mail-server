package handlers

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	dkim "github.com/rest-mail/go-dkim"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/outbound"
)

// TestSentMessage_CarriesOneVerifyingDKIMSignature follows a message the way
// the API sends it: rendered with the headers the outbound pipeline's transforms
// added, signed over its wire bytes, then prepared by the queue worker for
// relay. It must leave with exactly one DKIM-Signature, made by the sender
// domain's key, which go-dkim verifies against the domain's published record.
// A signature a transform supplied (or a second one from the relay step) would
// show up here as a count above one. Needs the unit-test Postgres; skips when
// none is reachable.
func TestSentMessage_CarriesOneVerifyingDKIMSignature(t *testing.T) {
	gdb, err := connectTestDB()
	if err != nil {
		t.Skipf("sent-message DKIM test skipped: no database reachable (%v)", err)
	}
	if err := gdb.AutoMigrate(&models.Domain{}); err != nil {
		t.Skipf("sent-message DKIM test skipped: migrate failed (%v)", err)
	}
	tx := gdb.Begin()
	t.Cleanup(func() { tx.Rollback() })

	const masterKey = "sent-message-dkim-master-key"
	private, public, err := dkim.GenerateKey(2048)
	if err != nil {
		t.Fatalf("generate a DKIM key: %v", err)
	}
	stored, err := models.EncryptDKIMPrivateKey(private, masterKey)
	if err != nil {
		t.Fatalf("encrypt the DKIM key: %v", err)
	}
	domain := fmt.Sprintf("dkim-once-%d.test", time.Now().UnixNano())
	if err := tx.Create(&models.Domain{Name: domain, DKIMSelector: "rm1", DKIMPrivateKey: stored}).Error; err != nil {
		t.Fatalf("seed the domain: %v", err)
	}
	record, err := dkim.RecordValue(public)
	if err != nil {
		t.Fatalf("DKIM record: %v", err)
	}

	raw, err := buildOutboundRaw(outboundMessage{
		FromName:  "Alice",
		From:      "alice@" + domain,
		To:        []string{"bob@example.net"},
		Subject:   "Signed once",
		BodyText:  "Hello Bob,\r\nthis message is signed exactly once.\r\n",
		MessageID: "once@" + domain,
		Date:      time.Now(),
		// What a custom transform could add: none of these signatures was
		// computed over the rendered bytes.
		Extra: map[string]string{
			"DKIM-Signature": "v=1; a=rsa-sha256; d=" + domain + "; s=rm1; h=from; bh=x; b=forged",
			"ARC-Seal":       "i=1; a=rsa-sha256; cv=none; d=" + domain + "; s=rm1; b=forged",
			"X-Transformed":  "yes",
		},
	})
	if err != nil {
		t.Fatalf("render the message: %v", err)
	}

	h := &MessageHandler{db: tx, masterKey: masterKey}
	signed, err := h.signOutboundDKIM(domain, raw)
	if err != nil {
		t.Fatalf("sign the message: %v", err)
	}
	// The queue worker's last step before relay must leave the message alone.
	relayed, err := outbound.Prepare(tx, masterKey, domain, signed)
	if err != nil {
		t.Fatalf("prepare for relay: %v", err)
	}
	if relayed != signed {
		t.Fatal("the relay step changed an already-signed message")
	}

	headers, _ := dkim.SplitMessage([]byte(relayed))
	signatures, transformed := 0, false
	for _, hdr := range headers {
		switch strings.ToLower(hdr.Name) {
		case "dkim-signature":
			signatures++
		case "x-transformed":
			transformed = true
		}
		if strings.HasPrefix(strings.ToLower(hdr.Name), "arc-") {
			t.Errorf("the sent message carries %s, which nothing computed over it", hdr.Name)
		}
	}
	if signatures != 1 {
		t.Fatalf("the sent message carries %d DKIM-Signature headers, want exactly 1:\n%s", signatures, relayed)
	}
	if !transformed {
		t.Error("a transform's ordinary header was dropped along with the signatures")
	}

	resolver := func(_ context.Context, name string) ([]string, error) {
		if name == "rm1._domainkey."+domain {
			return []string{record}, nil
		}
		return nil, fmt.Errorf("no TXT record for %s", name)
	}
	results := dkim.Verify(context.Background(), []byte(relayed), resolver)
	if len(results) != 1 {
		t.Fatalf("go-dkim found %d signatures, want 1: %+v", len(results), results)
	}
	if got := results[0]; got.Result != dkim.ResultPass || got.Domain != domain || got.Selector != "rm1" {
		t.Fatalf("go-dkim verdict = %+v, want pass for d=%s s=rm1", got, domain)
	}
}
