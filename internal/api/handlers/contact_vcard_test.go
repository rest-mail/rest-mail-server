package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
)

// readVCardFile is what eachVCardContact gives for a file: its contacts, the problems found
// and the count it answers.
func readVCardFile(t *testing.T, file string) ([]models.Contact, map[string]string, int, error) {
	t.Helper()
	var contacts []models.Contact
	problems := map[string]string{}
	total, err := eachVCardContact(strings.NewReader(file), func(contact models.Contact, found map[string]string) error {
		contacts = append(contacts, contact)
		for field, problem := range found {
			problems[field] = problem
		}
		return nil
	})
	return contacts, problems, total, err
}

// The shapes address books write: Apple's grouped properties and folded lines, the LF line
// endings and byte-order mark some tools add, vCard 2.1's bare parameters, and properties
// REST Mail does not store, some of them far larger than anything it keeps.
func TestVCardFromOtherAddressBooks(t *testing.T) {
	photo := strings.Repeat("QUJD", 200_000)
	for _, tc := range []struct {
		name, file string
		want       [][2]string
		total      int
	}{
		{"vCard 3.0 with groups and a fold", "BEGIN:VCARD\r\nVERSION:3.0\r\nN:Doe;Jane;;;\r\nFN:Jane\r\n  Doe\r\nitem1.EMAIL;type=INTERNET;type=pref:jane@example.org\r\nitem1.X-ABLabel:_$!<Home>!$_\r\nTEL;type=CELL:+1 555 0100\r\nEND:VCARD\r\n",
			[][2]string{{"jane@example.org", "Jane Doe"}}, 1},
		{"vCard 4.0, two addresses, escapes", "BEGIN:VCARD\nVERSION:4.0\nFN:Doe\\, Jane\\; Jr\nEMAIL;TYPE=work:jane@example.org\nEMAIL;PREF=1:j.doe@example.org\nEND:VCARD\n",
			[][2]string{{"jane@example.org", "Doe, Jane; Jr"}, {"j.doe@example.org", "Doe, Jane; Jr"}}, 2},
		{"vCard 2.1 with a byte-order mark", "\xef\xbb\xbfBEGIN:VCARD\r\nVERSION:2.1\r\nFN:Ann\r\nEMAIL;PREF;INTERNET:ann@example.org\r\nEND:VCARD\r\n",
			[][2]string{{"ann@example.org", "Ann"}}, 1},
		{"lower-case names and blank lines between cards", "begin:vcard\r\nversion:3.0\r\nfn:Bo\r\nemail:bo@example.org\r\nend:vcard\r\n\r\nBEGIN:VCARD\r\nVERSION:3.0\r\nFN:Cy\r\nEMAIL:cy@example.org\r\nEND:VCARD\r\n",
			[][2]string{{"bo@example.org", "Bo"}, {"cy@example.org", "Cy"}}, 2},
		{"a card with no address is counted and adds nothing", "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Phone Only\r\nTEL:+1 555 0100\r\nEND:VCARD\r\nBEGIN:VCARD\r\nVERSION:3.0\r\nFN:Di\r\nEMAIL:di@example.org\r\nEND:VCARD\r\n",
			[][2]string{{"di@example.org", "Di"}}, 2},
		{"a huge unfolded photo is passed over", "BEGIN:VCARD\r\nVERSION:3.0\r\nPHOTO;ENCODING=b;TYPE=JPEG:" + photo + "\r\nFN:Ed\r\nEMAIL:ed@example.org\r\nEND:VCARD\r\n",
			[][2]string{{"ed@example.org", "Ed"}}, 1},
		{"a quoted-printable note cannot end the card or add an address", "BEGIN:VCARD\r\nVERSION:2.1\r\nFN:Flo\r\nNOTE;ENCODING=QUOTED-PRINTABLE:first=\r\nEMAIL:planted@example.org=\r\nEND:VCARD\r\nEMAIL:flo@example.org\r\nEND:VCARD\r\n",
			[][2]string{{"flo@example.org", "Flo"}}, 1},
		{"a quoted parameter may hold a colon", "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Gus\r\nEMAIL;PID=\"a:b;c\":gus@example.org\r\nEND:VCARD\r\n",
			[][2]string{{"gus@example.org", "Gus"}}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			contacts, problems, total, err := readVCardFile(t, tc.file)
			if err != nil || len(problems) != 0 {
				t.Fatalf("err %v, problems %v", err, problems)
			}
			if total != tc.total || len(contacts) != len(tc.want) {
				t.Fatalf("total %d, contacts %+v, want %d and %v", total, contacts, tc.total, tc.want)
			}
			for index, want := range tc.want {
				got := contacts[index]
				if got.Email != want[0] || got.Name != want[1] || got.TrustLevel != "auto" || got.Source != "import" {
					t.Errorf("contact %d = %+v, want %v as auto, import", index, got, want)
				}
				if !got.CreatedAt.IsZero() || !got.UpdatedAt.IsZero() {
					t.Errorf("contact %d has times no card gave: %+v", index, got)
				}
			}
		})
	}
}

// What REST Mail's own export adds comes back; an FN that only repeats the address is the
// export's stand-in for no name.
func TestVCardRestoresRestMailProperties(t *testing.T) {
	contacts, problems, _, err := readVCardFile(t, "BEGIN:VCARD\r\nVERSION:4.0\r\nUID:https://mail.example/api/v1/accounts/1/contacts/9\r\nFN:a@example.org\r\nEMAIL:a@example.org\r\n"+
		"X-RESTMAIL-TRUST-LEVEL:blocked\r\nX-RESTMAIL-SOURCE:manual\r\nX-RESTMAIL-CREATED-AT:2024-02-29T12:30:15.123456Z\r\nREV:20240301T000000Z\r\nX-RESTMAIL-UPDATED-AT:2024-03-01T01:00:00+01:00\r\nEND:VCARD\r\n")
	if err != nil || len(problems) != 0 || len(contacts) != 1 {
		t.Fatalf("contacts %+v, problems %v, err %v", contacts, problems, err)
	}
	got := contacts[0]
	if got.Name != "" || got.TrustLevel != "blocked" || got.Source != "manual" ||
		!got.CreatedAt.Equal(time.Date(2024, 2, 29, 12, 30, 15, 123456000, time.UTC)) ||
		!got.UpdatedAt.Equal(time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("contact = %+v", got)
	}
}

func TestVCardProblemsNameTheCard(t *testing.T) {
	card := func(lines ...string) string {
		return "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Good\r\nEMAIL:good@example.org\r\nEND:VCARD\r\nBEGIN:VCARD\r\n" + strings.Join(lines, "\r\n") + "\r\nEND:VCARD\r\n"
	}
	manyAddresses := make([]string, maxVCardAddresses+1)
	for index := range manyAddresses {
		manyAddresses[index] = fmt.Sprintf("EMAIL:p%d@example.org", index)
	}
	for _, tc := range []struct{ name, file, field, problem string }{
		{"bad address", card("EMAIL:not-an-address"), "cards[1].email", "must be an email address"},
		{"name and address form", card("EMAIL:Jane <jane@example.org>"), "cards[1].email", "must be an email address"},
		{"name too long", card("FN:"+strings.Repeat("n", 256), "EMAIL:n@example.org"), "cards[1].name", ""},
		{"name past what is kept", card("FN:"+strings.Repeat("n", maxVCardProperty+1), "EMAIL:n@example.org"), "cards[1].name", "is too long"},
		{"quoted-printable name", card("FN;ENCODING=QUOTED-PRINTABLE:J=C3=A9", "EMAIL:j@example.org"), "cards[1].name", "uses an encoding or character set other than UTF-8 text"},
		{"bare quoted-printable", card("FN;QUOTED-PRINTABLE:J=C3=A9", "EMAIL:j@example.org"), "cards[1].name", "uses an encoding or character set other than UTF-8 text"},
		{"another character set", card("FN;CHARSET=ISO-8859-1:Jos\xe9", "EMAIL:j@example.org"), "cards[1].name", "uses an encoding or character set other than UTF-8 text"},
		{"not UTF-8", card("FN:Jos\xe9", "EMAIL:j@example.org"), "cards[1].name", "must be UTF-8 text"},
		{"NUL in a name", card("FN:a\x00b", "EMAIL:j@example.org"), "cards[1].name", "must not contain control characters"},
		{"unknown version", card("VERSION:5.0", "EMAIL:j@example.org"), "cards[1].version", "must be 2.1, 3.0 or 4.0"},
		{"unknown trust level", card("EMAIL:j@example.org", "X-RESTMAIL-TRUST-LEVEL:owner"), "cards[1].trust_level", "must be auto, trusted, or blocked"},
		{"unknown source", card("EMAIL:j@example.org", "X-RESTMAIL-SOURCE:spam"), "cards[1].source", "must be sent, manual, or import"},
		{"unreadable time", card("EMAIL:j@example.org", "X-RESTMAIL-CREATED-AT:yesterday"), "cards[1].created_at", "must be an RFC 3339 time"},
		{"year zero", card("EMAIL:j@example.org", "X-RESTMAIL-CREATED-AT:0000-06-01T00:00:00Z"), "cards[1].created_at", "must be an RFC 3339 time"},
		{"year past the API's", card("EMAIL:j@example.org", "X-RESTMAIL-UPDATED-AT:9999-12-31T23:59:59-23:00"), "cards[1].updated_at", "must be an RFC 3339 time"},
		{"too many addresses", card(manyAddresses...), "cards[1].email", "must be at most 100 addresses"},
		{"a wrong card with no address", card("X-RESTMAIL-SOURCE:spam"), "cards[1].source", "must be sent, manual, or import"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, problems, _, err := readVCardFile(t, tc.file)
			if err != nil {
				t.Fatal(err)
			}
			problem, found := problems[tc.field]
			if !found || (tc.problem != "" && problem != tc.problem) {
				t.Fatalf("problems = %v, want %s: %q", problems, tc.field, tc.problem)
			}
		})
	}
}

func TestVCardFileThatIsNotCardsIsRefused(t *testing.T) {
	for name, file := range map[string]string{
		"text":              "not a card\r\n",
		"JSON":              `{"contacts":[{"email":"a@example.org"}]}`,
		"property outside":  "EMAIL:a@example.org\r\nBEGIN:VCARD\r\nEND:VCARD\r\n",
		"end without begin": "END:VCARD\r\n",
		"never ended":       "BEGIN:VCARD\r\nEMAIL:a@example.org\r\n",
		"card inside card":  "BEGIN:VCARD\r\nBEGIN:VCARD\r\nEND:VCARD\r\nEND:VCARD\r\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, _, _, err := readVCardFile(t, file)
			var refusal *endpoint.Error
			if !errors.As(err, &refusal) || refusal.Status != http.StatusUnprocessableEntity || refusal.Fields["body"] == "" {
				t.Fatalf("err = %#v, want a 422 naming the body", err)
			}
		})
	}
	if contacts, problems, total, err := readVCardFile(t, ""); err != nil || total != 0 || len(contacts)+len(problems) != 0 {
		t.Fatalf("empty file: %v %v %d %v", contacts, problems, total, err)
	}
}

// The route: both forms reach the same rows and limits, a bad card anywhere leaves nothing
// behind, and another media type is refused naming the two the route takes.
func TestImportContactsTakesAVCardFile(t *testing.T) {
	db := openAuthzTestDB(t)
	if err := migrateModels(db, &models.ServerLimits{}, &models.Contact{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_contacts_mailbox_email ON contacts(mailbox_id, email)`).Error; err != nil {
		t.Fatal(err)
	}
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })
	fx := seedAuthzFixture(t, tx)
	if err := tx.Create(&models.Contact{MailboxID: fx.ownerMailbox.ID, Email: "kept@example.org", Name: "Kept", TrustLevel: "blocked", Source: "manual"}).Error; err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	NewContactHandler(tx).RegisterContacts(endpoint.Routes{Router: router, Registry: endpoint.NewRegistry()})
	path := "/api/v1/accounts/" + uintToStr(fx.ownerMailbox.ID) + "/contacts/import"
	send := func(media, body string) *httptest.ResponseRecorder {
		request := authzRequest(http.MethodPost, path, fx.ownerMailbox.ID, nil, body)
		request.Header.Set("Content-Type", media)
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		return recorder
	}
	rows := func() []string {
		var contacts []models.Contact
		if err := tx.Where("mailbox_id = ?", fx.ownerMailbox.ID).Order("email").Find(&contacts).Error; err != nil {
			t.Fatal(err)
		}
		out := []string{}
		for _, contact := range contacts {
			out = append(out, strings.Join([]string{contact.Email, contact.Name, contact.TrustLevel, contact.Source}, "|"))
		}
		return out
	}
	before := fmt.Sprint(rows())

	bad := "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:New\r\nEMAIL:new@example.org\r\nEND:VCARD\r\nBEGIN:VCARD\r\nVERSION:3.0\r\nEMAIL:nope\r\nEND:VCARD\r\n"
	if rr := send("text/vcard", bad); rr.Code != http.StatusUnprocessableEntity || !strings.Contains(rr.Body.String(), `"cards[1].email"`) {
		t.Fatalf("bad card: %d %s", rr.Code, rr.Body)
	}
	if got := fmt.Sprint(rows()); got != before {
		t.Fatalf("a refused file changed the contacts: %s", got)
	}
	if rr := send("text/vcard", "BEGIN:VCARD\r\nEND"); rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unfinished file: %d %s", rr.Code, rr.Body)
	}
	if rr := send("text/vcard", ""); rr.Code != http.StatusUnprocessableEntity || !strings.Contains(rr.Body.String(), `"contacts"`) {
		t.Fatalf("empty file: %d %s", rr.Code, rr.Body)
	}
	if rr := send("text/plain", "BEGIN:VCARD\r\nEND:VCARD\r\n"); rr.Code != http.StatusBadRequest ||
		!strings.Contains(rr.Body.String(), "the body is taken as application/json, text/vcard") {
		t.Fatalf("another media type: %d %s", rr.Code, rr.Body)
	}
	if rr := send("application/json", "BEGIN:VCARD\r\nEND:VCARD\r\n"); rr.Code != http.StatusBadRequest {
		t.Fatalf("a card sent as JSON: %d %s", rr.Code, rr.Body)
	}

	good := "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:New\r\nEMAIL:new@example.org\r\nEMAIL:kept@example.org\r\nEND:VCARD\r\nBEGIN:VCARD\r\nVERSION:3.0\r\nFN:Phone\r\nTEL:1\r\nEND:VCARD\r\n"
	rr := send("text/vcard; charset=utf-8", good)
	if rr.Code != http.StatusOK {
		t.Fatalf("%d %s", rr.Code, rr.Body)
	}
	var answer struct {
		Data ContactsImported `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &answer); err != nil {
		t.Fatal(err)
	}
	if answer.Data != (ContactsImported{Imported: 1, Skipped: 2}) {
		t.Fatalf("answer = %+v", answer.Data)
	}
	want := "[kept@example.org|Kept|blocked|manual new@example.org|New|auto|import]"
	if got := fmt.Sprint(rows()); got != want {
		t.Fatalf("contacts = %s, want %s", got, want)
	}

	// A file over the server's limit is refused whatever its form.
	if err := tx.Save(&models.ServerLimits{ID: serverLimitsRowID, ContactImportMaxBytes: 64}).Error; err != nil {
		t.Fatal(err)
	}
	if rr := send("text/vcard", good); rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("over the limit: %d %s", rr.Code, rr.Body)
	}
}
