package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/vcard"
)

func TestVCardContactDetailsRoundTrip(t *testing.T) {
	notes := strings.Repeat("午後 ☀️ ", 20) + "call, then confirm; use \\work\nSecond line"
	card := "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Example Person\r\nEMAIL:person@example.test\r\n" +
		"item1.TEL;TYPE=\"work,voice\";VALUE=uri:tel:+15550100;ext=42\r\n" +
		"TEL;TYPE=home:+1 555 0101\r\nTEL;TYPE=cell:+1 555 0102\r\n" +
		"TEL:+1 555 0103\r\nORG:Example\\, Inc;Research\\; Design\r\n" +
		"NOTE:" + strings.Repeat("午後 ☀️ ", 10) + "\r\n " + strings.Repeat("午後 ☀️ ", 10) +
		"call\\, then confirm\\; use \\\\work\\nSecond line\r\nEND:VCARD\r\n"
	read := func(file string) models.Contact {
		t.Helper()
		got, problems, count, err := readVCardFile(t, file)
		if err != nil || count != 1 || len(got) != 1 || len(problems) != 0 {
			t.Fatalf("read vCard: %v, count %d, problems %v", err, count, problems)
		}
		return got[0]
	}
	want := models.Contact{
		Name: "Example Person", Email: "person@example.test", TrustLevel: "auto", Source: "import",
		PhoneNumbers: []vcard.PhoneNumber{
			{Number: "tel:+15550100;ext=42", Types: []string{"work", "voice"}},
			{Number: "+1 555 0101", Types: []string{"home"}},
			{Number: "+1 555 0102", Types: []string{"cell"}},
			{Number: "+1 555 0103"},
		},
		Organization: "Example, Inc;Research; Design", Notes: notes,
	}
	imported := read(card)
	if !reflect.DeepEqual(imported, want) {
		t.Fatalf("imported contact = %+v, want %+v", imported, want)
	}
	var export bytes.Buffer
	if err := vcard.WriteContact(&export, "urn:uuid:0c99f5a5-ea71-42b5-b421-4769bd94c39d", vcard.Contact{
		ID: 1, Name: imported.Name, Email: imported.Email, PhoneNumbers: imported.PhoneNumbers,
		Organization: imported.Organization, Notes: imported.Notes, TrustLevel: imported.TrustLevel, Source: imported.Source,
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(export.String(), "TEL;TYPE=work,voice;VALUE=uri:tel:+15550100;ext=42\r\n") {
		t.Fatalf("telephone URI lost on export: %q", export.String())
	}
	for line := range strings.SplitSeq(export.String(), "\r\n") {
		if len(line) > 75 || !utf8.ValidString(line) {
			t.Fatalf("invalid folded line: %q", line)
		}
	}
	if got := read(export.String()); !reflect.DeepEqual(got, want) {
		t.Fatalf("re-imported contact = %+v, want %+v", got, want)
	}
}

func TestVCardContactDetailsLimits(t *testing.T) {
	for _, tc := range []struct{ field, properties string }{
		{"phone_numbers", strings.Repeat("TEL:+1 555 0100\r\n", 21)},
		{"phone_numbers[0].number", "TEL:" + strings.Repeat("1", 129) + "\r\n"},
		{"phone_numbers[0].types", "TEL;TYPE=a,b,c,d,e,f,g,h,i:123\r\n"},
		{"phone_numbers[0].types[0]", "TEL;TYPE=" + strings.Repeat("x", 33) + ":123\r\n"},
		{"organization", "ORG:" + strings.Repeat("a", 256) + "\r\n"},
		{"notes", "NOTE:" + strings.Repeat("𐐀", 4097) + "\r\n"},
	} {
		t.Run(tc.field, func(t *testing.T) {
			card := "BEGIN:VCARD\r\nVERSION:4.0\r\nEMAIL:person@example.test\r\n" + tc.properties + "END:VCARD\r\n"
			_, problems, _, err := readVCardFile(t, card)
			if err != nil || problems["cards[0]."+tc.field] == "" {
				t.Fatalf("want refusal for %s, got %v (%v)", tc.field, problems, err)
			}
		})
	}
	card := "BEGIN:VCARD\r\nVERSION:4.0\r\nEMAIL:person@example.test\r\nNOTE:" + strings.Repeat("𐐀", 4096) + "\r\nEND:VCARD\r\n"
	if _, problems, _, err := readVCardFile(t, card); err != nil || len(problems) != 0 {
		t.Fatalf("note at limit refused: %v (%v)", problems, err)
	}
}

func TestContactDetailsLimits(t *testing.T) {
	routes := contactRoutes(NewContactHandler(nil))
	for _, tc := range []struct{ field, value string }{
		{"phone_numbers", "[" + strings.Repeat(`{"number":"123"},`, 20) + `{"number":"123"}]`},
		{"phone_numbers[0].number", fmt.Sprintf(`[{"number":%q}]`, strings.Repeat("1", 129))},
		{"phone_numbers[0].number", `[{"number":""}]`},
		{"phone_numbers[0].number", `[{"number":"123\nEND:VCARD"}]`},
		{"phone_numbers[0].types", `[{"number":"123","types":["a","b","c","d","e","f","g","h","i"]}]`},
		{"organization", fmt.Sprintf("%q", strings.Repeat("ü", 256))},
		{"notes", fmt.Sprintf("%q", strings.Repeat("é", 4097))},
		{"notes", `"null byte \u0000"`},
	} {
		for _, method := range []string{http.MethodPost, http.MethodPatch} {
			t.Run(method+"/"+tc.field, func(t *testing.T) {
				field := strings.SplitN(tc.field, "[", 2)[0]
				path, body := "/api/v1/accounts/1/contacts/1", fmt.Sprintf(`{%q:%s}`, field, tc.value)
				if method == http.MethodPost {
					path, body = "/api/v1/accounts/1/contacts", fmt.Sprintf(`{"email":"person@example.test",%q:%s}`, field, tc.value)
				}
				rr := httptest.NewRecorder()
				routes.ServeHTTP(rr, httptest.NewRequest(method, path, strings.NewReader(body)))
				if rr.Code != http.StatusUnprocessableEntity || !strings.Contains(rr.Body.String(), `"`+tc.field+`":`) {
					t.Fatalf("want 422 for %s, got %d %s", tc.field, rr.Code, rr.Body)
				}
			})
		}
	}
}

func TestContactDetailsPersistence(t *testing.T) {
	if os.Getenv("DB_HOST") == "" {
		t.Skip("set DB_HOST to test contact persistence")
	}
	db := openAuthzTestDB(t)
	if err := migrateModels(db, &models.Contact{}); err != nil {
		t.Fatal(err)
	}
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	fixture := seedAuthzFixture(t, tx)
	routes := contactRoutes(NewContactHandler(tx))
	send := func(method, suffix, body string, status int) []byte {
		t.Helper()
		rr := httptest.NewRecorder()
		request := authzRequest(method, contactsPathOf(fixture.ownerMailbox.ID, suffix), fixture.ownerMailbox.ID, nil, body)
		request.Header.Set("Content-Type", "application/json")
		routes.ServeHTTP(rr, request)
		if rr.Code != status {
			t.Fatalf("%s %s: got %d %s, want %d", method, suffix, rr.Code, rr.Body, status)
		}
		return rr.Body.Bytes()
	}
	check := func(body []byte, want ContactDetails) Contact {
		t.Helper()
		var response struct{ Data Contact }
		if err := json.Unmarshal(body, &response); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(response.Data.ContactDetails, want) {
			t.Fatalf("contact details = %+v, want %+v", response.Data.ContactDetails, want)
		}
		return response.Data
	}
	original := ContactDetails{
		PhoneNumbers: []vcard.PhoneNumber{{Number: "+1 555 0100", Types: []string{"work"}}},
		Organization: "Example Studio", Notes: "Afternoons",
	}
	created := check(send(http.MethodPost, "", `{"email":"details@example.test","phone_numbers":[{"number":"+1 555 0100","types":["work"]}],"organization":"Example Studio","notes":"Afternoons"}`, 201), original)
	path := "/" + uintToStr(created.ID)
	check(send(http.MethodGet, path, "", 200), original)
	for _, method := range []string{http.MethodPost, http.MethodPatch} {
		for _, token := range []string{"work;NOTE", strings.Repeat("x", 33)} {
			suffix, body := path, fmt.Sprintf(`{"phone_numbers":[{"number":"123","types":[%q]}]}`, token)
			if method == http.MethodPost {
				suffix, body = "", strings.Replace(body, "{", `{"email":"invalid-details@example.test",`, 1)
			}
			if raw := send(method, suffix, body, 422); !bytes.Contains(raw, []byte(`"phone_numbers[0].types[0]"`)) {
				t.Fatalf("invalid type did not name the field: %s", raw)
			}
		}
	}
	changed := ContactDetails{
		PhoneNumbers: []vcard.PhoneNumber{{Number: "tel:+15550101;ext=42", Types: []string{"work", "voice"}}, {Number: "456"}},
		Organization: original.Organization, Notes: "First\nSecond",
	}
	check(send(http.MethodPatch, path, `{"phone_numbers":[{"number":"tel:+15550101;ext=42","types":["work","voice"]},{"number":"456"}],"notes":"First\nSecond"}`, 200), changed)
	check(send(http.MethodPatch, path, `{"phone_numbers":null,"organization":null,"notes":null}`, 200), changed)
	var list struct{ Data []Contact }
	if err := json.Unmarshal(send(http.MethodGet, "", "", 200), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Data) != 1 || !reflect.DeepEqual(list.Data[0].ContactDetails, changed) {
		t.Fatalf("list after update: %+v", list.Data)
	}
	var stored models.Contact
	if err := tx.First(&stored, created.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(stored.PhoneNumbers, changed.PhoneNumbers) || stored.Organization != changed.Organization || stored.Notes != changed.Notes {
		t.Fatalf("persisted contact: %+v", stored)
	}
	check(send(http.MethodPatch, path, `{"phone_numbers":[],"organization":"","notes":""}`, 200), ContactDetails{})
	check(send(http.MethodGet, path, "", 200), ContactDetails{})
}
