package handlers

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/vcard"
)

// TestWebmailContactsExportImportsEveryField reads the webmail's contacts export fixture
// (webmail/src/lib/contactsVCard.test.ts writes it) the way importContacts does, with the
// trust level restored, and requires every stored field back as it was exported.
func TestWebmailContactsExportImportsEveryField(t *testing.T) {
	file, err := os.Open("../../../webmail/src/lib/testdata/contacts-export.vcf")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	at := func(value string) time.Time {
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			t.Fatal(err)
		}
		return parsed
	}
	want := []models.Contact{
		{Email: "ada@example.com", Name: "Lovelace, Ada", TrustLevel: "trusted", Source: "manual",
			PhoneNumbers: []vcard.PhoneNumber{
				{Number: "tel:+15550100;ext=42", Types: []string{"work", "voice"}},
				{Number: "+1 555 0101", Types: []string{"home"}},
			},
			Organization: "Example, Inc;Research", Notes: "Afternoons\nCall, then email.",
			CreatedAt: at("2026-11-20T10:11:12.123456Z"), UpdatedAt: at("2026-11-21T08:09:10.654321Z")},
		{Email: "bob@example.com", Name: "bob@example.com", TrustLevel: "blocked", Source: "sent",
			CreatedAt: at("2026-11-20T10:11:12.5Z"), UpdatedAt: at("2026-11-20T10:11:12.5Z")},
		{Email: "cy@example.com", TrustLevel: "auto", Source: "import",
			CreatedAt: at("2026-11-20T10:11:12Z"), UpdatedAt: at("2026-11-20T10:11:12Z")},
		{Email: "dee@example.com", Name: "Ünïcødé " + strings.Repeat("é", 50), TrustLevel: "auto", Source: "manual",
			CreatedAt: at("2026-11-20T10:11:12Z"), UpdatedAt: at("2026-11-20T10:11:12Z")},
	}
	var got []models.Contact
	cards, err := eachVCardContact(file, true, func(contact models.Contact, problems map[string]string) error {
		if len(problems) > 0 {
			t.Errorf("%s was refused: %v", contact.Email, problems)
		}
		got = append(got, contact)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if cards != len(want) || len(got) != len(want) {
		t.Fatalf("read %d cards and %d contacts, want %d", cards, len(got), len(want))
	}
	for i, contact := range got {
		expected := want[i]
		if contact.Email != expected.Email || contact.Name != expected.Name || contact.TrustLevel != expected.TrustLevel ||
			!reflect.DeepEqual(contact.PhoneNumbers, expected.PhoneNumbers) || contact.Organization != expected.Organization || contact.Notes != expected.Notes ||
			contact.Source != expected.Source || !contact.CreatedAt.Equal(expected.CreatedAt) || !contact.UpdatedAt.Equal(expected.UpdatedAt) {
			t.Errorf("contact %d is %+v, want %+v", i, contact, expected)
		}
	}
}
