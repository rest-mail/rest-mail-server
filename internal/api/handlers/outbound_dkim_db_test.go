package handlers

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	dkim "github.com/rest-mail/go-dkim/pkg/dkim"
	"github.com/restmail/restmail/internal/db/models"
	rmail "github.com/restmail/restmail/internal/mail"
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
	h, domain, resolver := seedSigningDomain(t, "dkim-once")

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

	signed, err := h.signOutboundDKIM(domain, raw)
	if err != nil {
		t.Fatalf("sign the message: %v", err)
	}
	// The queue worker's last step before relay must leave the message alone.
	relayed, err := outbound.Prepare(h.db, h.masterKey, domain, signed)
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

	results := dkim.Verify(context.Background(), []byte(relayed), resolver)
	if len(results) != 1 {
		t.Fatalf("go-dkim found %d signatures, want 1: %+v", len(results), results)
	}
	if got := results[0]; got.Result != dkim.ResultPass || got.Domain != domain || got.Selector != "rm1" {
		t.Fatalf("go-dkim verdict = %+v, want pass for d=%s s=rm1", got, domain)
	}
}

// TestSentMessage_OversignsTheFieldsAReaderSees reads the h= of a sent
// message's signature (#128). The server signs with go-dkim's defaults: the
// RFC 6376 §5.4.1 set, with the fields a reader sees oversigned. An oversigned
// field the message carries once is named twice, so a second copy breaks the
// signature, and one it lacks is named once, which asserts its absence. Any
// other signed field is named once per instance, and nothing outside the
// defaults is signed. Needs the unit-test Postgres; skips when none is
// reachable.
func TestSentMessage_OversignsTheFieldsAReaderSees(t *testing.T) {
	h, domain, _ := seedSigningDomain(t, "dkim-oversign")
	signed := signSentMessage(t, h, domain)

	carried := map[string]int{}
	hTag := ""
	headers, _ := dkim.SplitMessage([]byte(signed))
	for _, hdr := range headers {
		name := strings.ToLower(hdr.Name)
		if name == "dkim-signature" {
			hTag = dkim.StripWSP(dkim.ParseTagList(hdr.Value)["h"])
			continue
		}
		carried[name]++
	}
	if hTag == "" {
		t.Fatalf("the sent message has no signature with an h= tag:\n%s", signed)
	}
	named := map[string]int{}
	for _, name := range strings.Split(hTag, ":") {
		named[name]++
	}

	oversigned := map[string]bool{}
	for _, name := range dkim.DefaultOversignedHeaders {
		oversigned[name] = true
		if got, want := named[name], carried[name]+1; got != want {
			t.Errorf("h= names %s %d times, want %d (the message carries %d)", name, got, want, carried[name])
		}
	}
	defaults := map[string]bool{}
	for _, name := range dkim.DefaultSignedHeaders {
		defaults[name] = true
		if !oversigned[name] && named[name] != carried[name] {
			t.Errorf("h= names %s %d times, want once per instance (%d)", name, named[name], carried[name])
		}
	}
	for name := range named {
		if !defaults[name] {
			t.Errorf("h= names %s, which go-dkim's defaults leave out", name)
		}
	}
	// The fixture carries what makes the checks above bite: each of these once.
	for _, name := range []string{"from", "to", "cc", "subject", "date", "message-id", "in-reply-to", "mime-version", "content-type"} {
		if carried[name] != 1 {
			t.Fatalf("the sent message carries %s %d times, want once:\n%s", name, carried[name], signed)
		}
	}
	if t.Failed() {
		t.Logf("h=%s", hTag)
	}
}

// TestSentMessage_AddedFieldBreaksTheSignature adds a field to a sent message
// after signing (#128): a second From or Subject, above the original or below
// it, or a Reply-To the message did not have. Each makes go-dkim's verdict
// fail, while the message as sent passes, and so does one with the Received
// field a relay adds, which is not signed. Needs the unit-test Postgres; skips
// when none is reachable.
func TestSentMessage_AddedFieldBreaksTheSignature(t *testing.T) {
	h, domain, resolver := seedSigningDomain(t, "dkim-added")
	signed := signSentMessage(t, h, domain)

	verdict := func(t *testing.T, msg string) dkim.VerifyResult {
		t.Helper()
		results := dkim.Verify(context.Background(), []byte(msg), resolver)
		if len(results) != 1 {
			t.Fatalf("go-dkim found %d signatures, want 1: %+v", len(results), results)
		}
		return results[0]
	}
	if got := verdict(t, signed); got.Result != dkim.ResultPass {
		t.Fatalf("the message as sent: %+v, want pass", got)
	}
	received := "Received: from mail." + domain + " by mx.example.net; " + time.Now().Format(time.RFC1123Z)
	if got := verdict(t, addHeaderField(signed, received, false)); got.Result != dkim.ResultPass {
		t.Fatalf("the message with a relay's Received field: %+v, want pass", got)
	}

	for _, tc := range []struct {
		name  string
		field string
		atEnd bool
	}{
		{"second From above", "From: Alice <alice@attacker.example>", false},
		{"second From below", "From: Alice <alice@attacker.example>", true},
		{"second Subject above", "Subject: Urgent: new bank details", false},
		{"second Subject below", "Subject: Urgent: new bank details", true},
		{"Reply-To it lacked", "Reply-To: alice@attacker.example", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tampered := addHeaderField(signed, tc.field, tc.atEnd)
			if got := verdict(t, tampered); got.Result != dkim.ResultFail {
				t.Fatalf("verdict = %+v, want fail:\n%s", got, tampered)
			}
		})
	}
}

// seedSigningDomain seeds a hosted domain with an encrypted DKIM key, in a
// transaction rolled back when the test ends. It returns a MessageHandler over
// that transaction, the domain's name, and a resolver that publishes the key
// record. It skips the test when no unit-test Postgres is reachable.
func seedSigningDomain(t *testing.T, prefix string) (*MessageHandler, string, dkim.TXTResolver) {
	t.Helper()
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
	domain := fmt.Sprintf("%s-%d.test", prefix, time.Now().UnixNano())
	if err := tx.Create(&models.Domain{Name: domain, DKIMSelector: "rm1", DKIMPrivateKey: stored}).Error; err != nil {
		t.Fatalf("seed the domain: %v", err)
	}
	record, err := dkim.RecordValue(public)
	if err != nil {
		t.Fatalf("DKIM record: %v", err)
	}
	resolver := func(_ context.Context, name string) ([]string, error) {
		if name == "rm1._domainkey."+domain {
			return []string{record}, nil
		}
		return nil, fmt.Errorf("no TXT record for %s", name)
	}
	return &MessageHandler{db: tx, masterKey: masterKey}, domain, resolver
}

// signSentMessage renders a reply the way the API sends it, carrying one each
// of From, To, Cc, Subject, Date, Message-ID and In-Reply-To, and signs it
// with the domain's key.
func signSentMessage(t *testing.T, h *MessageHandler, domain string) string {
	t.Helper()
	raw, err := buildOutboundRaw(outboundMessage{
		FromName:  "Alice",
		From:      "alice@" + domain,
		To:        []string{"bob@example.net"},
		Cc:        []string{"carol@example.net"},
		Subject:   "Quarterly figures",
		BodyText:  "Hello Bob,\r\nthe figures are attached.\r\n",
		MessageID: "figures@" + domain,
		InReplyTo: "question@example.net",
		Date:      time.Now(),
	})
	if err != nil {
		t.Fatalf("render the message: %v", err)
	}
	signed, err := h.signOutboundDKIM(domain, raw)
	if err != nil {
		t.Fatalf("sign the message: %v", err)
	}
	return signed
}

// addHeaderField adds a field to a signed message as a relay, or an attacker,
// would: above every other field, or at the end of the header block.
func addHeaderField(msg, field string, atEnd bool) string {
	if !atEnd {
		return field + "\r\n" + msg
	}
	i := strings.Index(msg, "\r\n\r\n")
	return msg[:i+2] + field + "\r\n" + msg[i+2:]
}

// Both the API and queued SMTP paths must sign EAI mail from hosted IDNs.
func TestSentMessage_IDNDKIM(t *testing.T) {
	h, domain, unicodeResolver := seedSigningDomain(t, "bücher")
	ascii, err := rmail.DomainASCII(domain)
	if err != nil {
		t.Fatal(err)
	}
	raw := "From: 用户@" + domain + "\r\nTo: müller@例え.jp\r\nSubject: Grüße 世界\r\n\r\nUTF-8 body: café\r\n"
	resolver := func(ctx context.Context, name string) ([]string, error) {
		if name != "rm1._domainkey."+ascii {
			t.Fatalf("key lookup %q, want A-label domain %q", name, ascii)
		}
		return unicodeResolver(ctx, "rm1._domainkey."+domain)
	}
	for _, path := range []string{"api", "queue"} {
		t.Run(path, func(t *testing.T) {
			var signed string
			var err error
			if path == "api" {
				signed, err = h.signOutboundDKIM(domain, raw)
			} else {
				signed, err = outbound.Prepare(h.db, h.masterKey, domain, raw)
			}
			if err != nil {
				t.Fatalf("sign IDN message: %v", err)
			}
			if !strings.HasSuffix(signed, "\r\n"+raw) {
				t.Fatal("signing changed original UTF-8 message bytes")
			}
			results := dkim.Verify(context.Background(), []byte(signed), resolver)
			if len(results) != 1 || results[0].Result != dkim.ResultPass || results[0].Domain != ascii {
				t.Fatalf("IDN signature did not verify with A-label domain: %+v", results)
			}
			tampered := strings.Replace(signed, "Subject: Grüße 世界", "Subject: forged", 1)
			if got := dkim.Verify(context.Background(), []byte(tampered), resolver); len(got) != 1 || got[0].Result != dkim.ResultFail {
				t.Fatalf("modified UTF-8 header was not detected: %+v", got)
			}
		})
	}
	var stored models.Domain
	if err := h.db.Where("name = ?", domain).First(&stored).Error; err != nil || stored.Name != domain {
		t.Fatalf("stored Unicode domain changed: %q, %v", stored.Name, err)
	}

	t.Run("invalid hosted IDN fails closed", func(t *testing.T) {
		invalid := "bad_." + domain
		if err := h.db.Model(&stored).Update("name", invalid).Error; err != nil {
			t.Fatal(err)
		}
		if got, err := outbound.SignDKIM(h.db, h.masterKey, invalid, raw); err == nil || got != "" {
			t.Fatalf("invalid hosted IDN signed or relayed unsigned: message length %d, error %v", len(got), err)
		}
	})
}
