package main

import (
	"bytes"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/vcard"
)

// webmailContactsFixture is what the webmail's contacts export writes for the contacts in
// TestWebmailContactsExportMatchesTheConsole (webmail/src/lib/contactsVCard.test.ts writes the
// same contacts and is held to the same file), so the two exports cannot drift apart.
const webmailContactsFixture = "../../webmail/src/lib/testdata/contacts-export.vcf"

func TestWebmailContactsExportMatchesTheConsole(t *testing.T) {
	at := func(value string) time.Time {
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			t.Fatal(err)
		}
		return parsed
	}
	base, err := url.Parse("https://mail.example")
	if err != nil {
		t.Fatal(err)
	}
	exporter := &mailExporter{base: base, account: 3}
	contacts := []exportedContact{
		{ID: 7, Email: "ada@example.com", Name: "Lovelace, Ada", TrustLevel: "trusted", Source: "manual",
			PhoneNumbers: []vcard.PhoneNumber{
				{Number: "tel:+15550100;ext=42", Types: []string{"work", "voice"}},
				{Number: "+1 555 0101", Types: []string{"home"}},
			},
			Organization: "Example, Inc;Research", Notes: "Afternoons\nCall, then email.",
			CreatedAt: at("2026-11-20T10:11:12.123456Z"), UpdatedAt: at("2026-11-21T08:09:10.654321Z")},
		{ID: 8, Email: "bob@example.com", Name: "bob@example.com", TrustLevel: "blocked", Source: "sent",
			CreatedAt: at("2026-11-20T10:11:12.5Z"), UpdatedAt: at("2026-11-20T10:11:12.5Z")},
		{ID: 9, Email: "cy@example.com", TrustLevel: "auto", Source: "import",
			CreatedAt: at("2026-11-20T10:11:12Z"), UpdatedAt: at("2026-11-20T10:11:12Z")},
		{ID: 10, Email: "dee@example.com", Name: "Ünïcødé " + string(bytes.Repeat([]byte("é"), 50)), TrustLevel: "auto", Source: "manual",
			CreatedAt: at("2026-11-20T10:11:12Z"), UpdatedAt: at("2026-11-20T10:11:12Z")},
	}
	var console bytes.Buffer
	for _, contact := range contacts {
		if err := writeVCard(&console, exporter, contact); err != nil {
			t.Fatal(err)
		}
	}
	webmail, err := os.ReadFile(webmailContactsFixture)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(console.Bytes(), webmail) {
		t.Fatalf("the console writes\n%q\nbut the webmail export writes\n%q", console.String(), string(webmail))
	}
}
