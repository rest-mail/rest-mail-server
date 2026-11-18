package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
)

// roundTripContact is every field the API gives for a stored contact, apart from the ids
// that belong to the account holding it.
type roundTripContact struct {
	Email      string    `json:"email"`
	Name       string    `json:"name"`
	TrustLevel string    `json:"trust_level"`
	Source     string    `json:"source"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// A file from export-contacts, imported into another server through the public API, gives
// back every field REST Mail stores, and exporting again gives the same cards (#1570).
func TestExportedContactsImportWithEveryStoredField(t *testing.T) {
	source := newMigrationServer(t)
	from := source.newAccount(t, "vcard-from@"+migrationDomain)
	created := time.Date(2024, 2, 29, 12, 30, 15, 123456000, time.UTC)
	seeded := []models.Contact{
		{Email: "blocked@example.test", Name: "Élodie, A;B\\C\nD", TrustLevel: "blocked", Source: "manual"},
		{Email: "unnamed@example.test", Name: "", TrustLevel: "auto", Source: "sent"},
		{Email: "trusted@example.test", Name: strings.Repeat("Long näme ", 20), TrustLevel: "trusted", Source: "import"},
	}
	for index := range seeded {
		seeded[index].MailboxID = from
		seeded[index].CreatedAt = created.Add(time.Duration(index) * time.Hour)
		seeded[index].UpdatedAt = created.Add(time.Duration(index) * 24 * time.Hour)
		if err := source.db.Create(&seeded[index]).Error; err != nil {
			t.Fatal(err)
		}
	}
	exported := exportContactsOf(t, source, from)

	destination := newMigrationServer(t)
	to := destination.newAccount(t, "vcard-to@"+migrationDomain)
	if got := importVCard(t, destination, to, destination.session, exported, http.StatusOK); got != `{"imported":3,"skipped":0}` {
		t.Fatalf("import: %s", got)
	}
	want := listRoundTripContacts(t, source, from)
	if len(want) != len(seeded) {
		t.Fatalf("source lists %d contacts, want %d", len(want), len(seeded))
	}
	if got := listRoundTripContacts(t, destination, to); !reflect.DeepEqual(got, want) {
		t.Fatalf("imported contacts differ:\n got %+v\nwant %+v", got, want)
	}
	if got, want := cardsWithoutUID(exportContactsOf(t, destination, to)), cardsWithoutUID(exported); got != want {
		t.Fatalf("exporting the import differs:\n got %q\nwant %q", got, want)
	}

	// The same file again adds nothing and changes nothing, so a contact blocked since is
	// not unblocked by restoring an old file.
	if err := destination.db.Model(&models.Contact{}).Where("mailbox_id = ? AND email = ?", to, "trusted@example.test").
		Update("trust_level", "blocked").Error; err != nil {
		t.Fatal(err)
	}
	if got := importVCard(t, destination, to, destination.session, exported, http.StatusOK); got != `{"imported":0,"skipped":3}` {
		t.Fatalf("second import: %s", got)
	}
	var trust string
	if err := destination.db.Model(&models.Contact{}).Where("mailbox_id = ? AND email = ?", to, "trusted@example.test").
		Pluck("trust_level", &trust).Error; err != nil || trust != "blocked" {
		t.Fatalf("existing contact's trust = %q (%v), want blocked", trust, err)
	}
}

// The vCard form is held to the access the JSON form is: mail:write on the account named,
// through the real router and credentials, with nothing stored when refused.
func TestVCardImportNeedsWriteAccessToTheAccount(t *testing.T) {
	server := newMigrationServer(t)
	owner := server.newAccount(t, "vcard-owner@"+migrationDomain)
	other := server.newAccount(t, "vcard-other@"+migrationDomain)
	card := "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Refused\r\nEMAIL:refused@example.test\r\nEND:VCARD\r\n"

	readOnly := dataExportKey(t, server, owner, "vcard-owner@"+migrationDomain, models.CapMailRead)
	otherWrite := dataExportKey(t, server, other, "vcard-other@"+migrationDomain, models.CapMailWrite)
	ownerWrite := dataExportKey(t, server, owner, "vcard-owner@"+migrationDomain, models.CapMailWrite)
	importVCard(t, server, owner, readOnly, card, http.StatusForbidden)
	// Another account's credential is not told the account exists.
	importVCard(t, server, owner, otherWrite, card, http.StatusNotFound)
	var stored int64
	if err := server.db.Model(&models.Contact{}).Where("email = ?", "refused@example.test").Count(&stored).Error; err != nil || stored != 0 {
		t.Fatalf("refused imports stored %d contacts (%v)", stored, err)
	}
	if got := importVCard(t, server, owner, ownerWrite, card, http.StatusOK); got != `{"imported":1,"skipped":0}` {
		t.Fatalf("permitted import: %s", got)
	}
}

func exportContactsOf(t *testing.T, server *migrationServer, account uint) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "contacts.vcf")
	if code, _, errs := exportDataAs(t, server, account, server.exportKey(t), "export-contacts", path); code != exitOK {
		t.Fatalf("export: exit %d: %s", code, errs)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// importVCard posts a file and answers the data of a 200, or the body of anything else.
func importVCard(t *testing.T, server *migrationServer, account uint, token, file string, status int) string {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/api/v1/accounts/%d/contacts/import", server.apiURL, account), strings.NewReader(file))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "text/vcard; charset=utf-8")
	response, err := server.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != status {
		t.Fatalf("import: HTTP %d, want %d: %s (%v)", response.StatusCode, status, body, err)
	}
	if status != http.StatusOK {
		return string(body)
	}
	var answer struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &answer); err != nil {
		t.Fatal(err)
	}
	return string(answer.Data)
}

func listRoundTripContacts(t *testing.T, server *migrationServer, account uint) []roundTripContact {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/api/v1/accounts/%d/contacts?limit=100", server.apiURL, account), nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+server.session)
	response, err := server.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	var answer struct {
		Data []roundTripContact `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&answer); err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("list contacts: HTTP %d (%v)", response.StatusCode, err)
	}
	byEmail := map[string]roundTripContact{}
	for _, contact := range answer.Data {
		byEmail[contact.Email] = contact
	}
	out := make([]roundTripContact, 0, len(answer.Data))
	for _, email := range []string{"blocked@example.test", "unnamed@example.test", "trusted@example.test"} {
		if contact, ok := byEmail[email]; ok {
			out = append(out, contact)
		}
	}
	return out
}

// cardsWithoutUID is an export's cards in address order, unfolded and without the UID, which
// names the server and contact id the card was exported from.
func cardsWithoutUID(file string) string {
	var cards []string
	for _, card := range strings.Split(strings.ReplaceAll(file, "\r\n ", ""), "END:VCARD\r\n") {
		var kept []string
		for _, line := range strings.Split(card, "\r\n") {
			if !strings.HasPrefix(line, "UID:") {
				kept = append(kept, line)
			}
		}
		cards = append(cards, strings.Join(kept, "\n"))
	}
	sort.Strings(cards)
	return strings.Join(cards, "\n--\n")
}
