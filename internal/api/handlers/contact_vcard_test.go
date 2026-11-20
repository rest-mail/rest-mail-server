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

// readVCardFile is what eachVCardContact gives for a file read as a default import, which
// does not restore trust levels: its contacts, the problems found and the count it answers.
func readVCardFile(t *testing.T, file string) ([]models.Contact, map[string]string, int, error) {
	t.Helper()
	return readVCardFileRestoring(t, file, false)
}

// readVCardFileRestoring is readVCardFile, saying whether trust levels are restored.
func readVCardFileRestoring(t *testing.T, file string, restoreTrust bool) ([]models.Contact, map[string]string, int, error) {
	t.Helper()
	var contacts []models.Contact
	problems := map[string]string{}
	total, err := eachVCardContact(strings.NewReader(file), restoreTrust, func(contact models.Contact, found map[string]string) error {
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
// export's stand-in for no name. The trust level comes back only when restoring it was asked
// for (#2101 AC05b); the other fields come back either way.
func TestVCardRestoresRestMailProperties(t *testing.T) {
	file := "BEGIN:VCARD\r\nVERSION:4.0\r\nUID:https://mail.example/api/v1/accounts/1/contacts/9\r\nFN:a@example.org\r\nEMAIL:a@example.org\r\n" +
		"X-RESTMAIL-TRUST-LEVEL:blocked\r\nX-RESTMAIL-SOURCE:manual\r\nX-RESTMAIL-CREATED-AT:2024-02-29T12:30:15.123456Z\r\nREV:20240301T000000Z\r\nX-RESTMAIL-UPDATED-AT:2024-03-01T01:00:00+01:00\r\nEND:VCARD\r\n"
	for _, tc := range []struct {
		restore bool
		trust   string
	}{{true, "blocked"}, {false, "auto"}} {
		contacts, problems, _, err := readVCardFileRestoring(t, file, tc.restore)
		if err != nil || len(problems) != 0 || len(contacts) != 1 {
			t.Fatalf("restore %v: contacts %+v, problems %v, err %v", tc.restore, contacts, problems, err)
		}
		got := contacts[0]
		if got.Name != "" || got.TrustLevel != tc.trust || got.Source != "manual" ||
			!got.CreatedAt.Equal(time.Date(2024, 2, 29, 12, 30, 15, 123456000, time.UTC)) ||
			!got.UpdatedAt.Equal(time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)) {
			t.Fatalf("restore %v: contact = %+v, want trust %s", tc.restore, got, tc.trust)
		}
	}
}

// A default import reads no trust level from a file, so a hostile card cannot trust or block a
// sender, whatever it claims and however it claims it (#2101 AC05b). A file that asks for an
// unknown level is refused only when the caller asked for levels to be restored.
func TestVCardDefaultImportCannotSetTrust(t *testing.T) {
	for _, line := range []string{
		"X-RESTMAIL-TRUST-LEVEL:trusted",
		"x-restmail-trust-level:trusted",
		"item1.X-RESTMAIL-TRUST-LEVEL:blocked",
		"X-RESTMAIL-TRUST-LEVEL;ENCODING=QUOTED-PRINTABLE:trusted",
		"X-RESTMAIL-TRUST-LEVEL:owner",
	} {
		file := "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Spoof\r\nEMAIL:spoof@example.org\r\nEMAIL:spoof2@example.org\r\n" + line + "\r\nEND:VCARD\r\n"
		contacts, problems, _, err := readVCardFile(t, file)
		if err != nil || len(problems) != 0 || len(contacts) != 2 {
			t.Fatalf("%s: contacts %+v, problems %v, err %v", line, contacts, problems, err)
		}
		for _, got := range contacts {
			if got.TrustLevel != "auto" {
				t.Errorf("%s: a default import stored %s as %s, want auto", line, got.Email, got.TrustLevel)
			}
		}
	}
	contacts, _, _, err := readVCardFileRestoring(t, "BEGIN:VCARD\r\nVERSION:3.0\r\nEMAIL:t@example.org\r\nx-restmail-trust-level:trusted\r\nEND:VCARD\r\n", true)
	if err != nil || len(contacts) != 1 || contacts[0].TrustLevel != "trusted" {
		t.Fatalf("an import asked to restore trust gave %+v (%v)", contacts, err)
	}
}

// A card with no FN, which vCard 2.1 allows, is named by its N, in the order a name is said:
// prefixes, given, additional and family names, then suffixes (#2101 AC05, #1570).
func TestVCardNameFromStructuredName(t *testing.T) {
	for _, tc := range []struct{ name, card, want string }{
		{"family and given", "N:Doe;Jane;;;", "Jane Doe"},
		{"every component", "N:Doe;Jane;Q.;Dr.;Jr.", "Dr. Jane Q. Doe Jr."},
		{"several values in one component", "N:Stevenson;John;Philip,Paul;Dr.;Jr.,M.D.,A.C.P.", "Dr. John Philip Paul Stevenson Jr. M.D. A.C.P."},
		{"escaped separators stay in the name", "N:O\\;Neil\\, Esq;Ann;;;", "Ann O;Neil, Esq"},
		{"only a family name", "N:Doe", "Doe"},
		{"an empty N leaves no name", "N:;;;;", ""},
		{"FN wins wherever it is", "N:Doe;Jane;;;\r\nFN:J. Doe", "J. Doe"},
		{"the first N is kept", "N:Doe;Jane;;;\r\nN:Roe;Rick;;;", "Jane Doe"},
		{"a quoted-printable N", "N;CHARSET=UTF-8;ENCODING=QUOTED-PRINTABLE:Dupont;=C3=89lodie;;;", "Élodie Dupont"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			contacts, problems, _, err := readVCardFile(t, "BEGIN:VCARD\r\nVERSION:2.1\r\n"+tc.card+"\r\nEMAIL:n@example.org\r\nEND:VCARD\r\n")
			if err != nil || len(problems) != 0 || len(contacts) != 1 {
				t.Fatalf("contacts %+v, problems %v, err %v", contacts, problems, err)
			}
			if contacts[0].Name != tc.want {
				t.Fatalf("name = %q, want %q", contacts[0].Name, tc.want)
			}
		})
	}
}

// vCard 2.1 address books, Android's among them, write a name that is not ASCII as
// quoted-printable UTF-8, often over several lines with soft line breaks. It is decoded; what
// it decodes to is held to the same rules as any other name (#2101 AC05, #1570).
func TestVCardQuotedPrintableIsDecoded(t *testing.T) {
	for _, tc := range []struct{ name, lines, want, email string }{
		{"Android's form", "FN;CHARSET=UTF-8;ENCODING=QUOTED-PRINTABLE:=C3=89lodie Dupont", "Élodie Dupont", "q@example.org"},
		{"bare parameter", "FN;QUOTED-PRINTABLE:J=C3=A9r=C3=B4me", "Jérôme", "q@example.org"},
		{"quoted encoding parameter", "FN;ENCODING=\"quoted-printable\":J=C3=A9r=C3=B4me", "Jérôme", "q@example.org"},
		{"soft line breaks", "FN;CHARSET=UTF-8;ENCODING=QUOTED-PRINTABLE:=E5=B1=B1=E7=94=B0=\r\n=E5=A4=AA=\r\n=E9=83=8E", "山田太郎", "q@example.org"},
		{"an encoded semicolon is part of the name", "FN;ENCODING=QUOTED-PRINTABLE:A=3BB", "A;B", "q@example.org"},
		{"a quoted-printable address", "FN:Q\r\nEMAIL;ENCODING=QUOTED-PRINTABLE:qp=40example.org", "Q", "qp@example.org"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := "BEGIN:VCARD\r\nVERSION:2.1\r\n" + tc.lines + "\r\n"
			if tc.email == "q@example.org" {
				file += "EMAIL:q@example.org\r\n"
			}
			contacts, problems, _, err := readVCardFile(t, file+"END:VCARD\r\n")
			if err != nil || len(problems) != 0 || len(contacts) != 1 {
				t.Fatalf("contacts %+v, problems %v, err %v", contacts, problems, err)
			}
			if contacts[0].Name != tc.want || contacts[0].Email != tc.email {
				t.Fatalf("contact = %q <%s>, want %q <%s>", contacts[0].Name, contacts[0].Email, tc.want, tc.email)
			}
		})
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
		{"quoted-printable that is not UTF-8", card("FN;ENCODING=QUOTED-PRINTABLE:Jos=E9", "EMAIL:j@example.org"), "cards[1].name", "must be UTF-8 text"},
		{"quoted-printable in another character set", card("FN;CHARSET=ISO-8859-1;ENCODING=QUOTED-PRINTABLE:Jos=E9", "EMAIL:j@example.org"), "cards[1].name", "uses an encoding or character set other than UTF-8 text"},
		{"broken quoted-printable", card("FN;ENCODING=QUOTED-PRINTABLE:J=ZZ", "EMAIL:j@example.org"), "cards[1].name", "is not valid quoted-printable"},
		{"quoted-printable control character", card("FN;ENCODING=QUOTED-PRINTABLE:a=00b", "EMAIL:j@example.org"), "cards[1].name", "must not contain control characters"},
		{"base64 name", card("FN;ENCODING=b:SsOp", "EMAIL:j@example.org"), "cards[1].name", "uses an encoding or character set other than UTF-8 text"},
		{"bare base64 name", card("FN;BASE64:SsOp", "EMAIL:j@example.org"), "cards[1].name", "uses an encoding or character set other than UTF-8 text"},
		{"another character set", card("FN;CHARSET=ISO-8859-1:Jos\xe9", "EMAIL:j@example.org"), "cards[1].name", "uses an encoding or character set other than UTF-8 text"},
		{"N not UTF-8", card("N:Jos\xe9;Ann;;;", "EMAIL:j@example.org"), "cards[1].name", "must be UTF-8 text"},
		{"N with a control character", card("N:a\x01b;Ann;;;", "EMAIL:j@example.org"), "cards[1].name", "must not contain control characters"},
		{"name from N too long", card("N:"+strings.Repeat("n", 200)+";"+strings.Repeat("g", 100)+";;;", "EMAIL:n@example.org"), "cards[1].name", ""},
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
			_, problems, _, err := readVCardFileRestoring(t, tc.file, true)
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

// Through the route, a file's trust levels reach the stored contacts only when the caller
// asks with restore_trust_level=true; a default import stores every new contact as auto, and
// the JSON form, which carries no trust level, is auto either way (#2101 AC05b).
func TestImportRestoresTrustLevelOnlyWhenAsked(t *testing.T) {
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
	router := chi.NewRouter()
	NewContactHandler(tx).RegisterContacts(endpoint.Routes{Router: router, Registry: endpoint.NewRegistry()})
	send := func(query, media, body string) *httptest.ResponseRecorder {
		path := "/api/v1/accounts/" + uintToStr(fx.ownerMailbox.ID) + "/contacts/import" + query
		request := authzRequest(http.MethodPost, path, fx.ownerMailbox.ID, nil, body)
		request.Header.Set("Content-Type", media)
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		return recorder
	}
	trustOf := func(email string) string {
		var trust []string
		if err := tx.Model(&models.Contact{}).Where("mailbox_id = ? AND email = ?", fx.ownerMailbox.ID, email).Pluck("trust_level", &trust).Error; err != nil || len(trust) != 1 {
			t.Fatalf("%s: %v (%v)", email, trust, err)
		}
		return trust[0]
	}
	card := func(email, trust string) string {
		return "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:" + email + "\r\nEMAIL:" + email + "\r\nX-RESTMAIL-TRUST-LEVEL:" + trust + "\r\nX-RESTMAIL-SOURCE:manual\r\nEND:VCARD\r\n"
	}

	for _, tc := range []struct{ query, email, file, want string }{
		{"", "default-trusted@example.org", card("default-trusted@example.org", "trusted"), "auto"},
		{"", "default-blocked@example.org", card("default-blocked@example.org", "blocked"), "auto"},
		{"?restore_trust_level=false", "false-trusted@example.org", card("false-trusted@example.org", "trusted"), "auto"},
		{"?restore_trust_level=true", "asked-trusted@example.org", card("asked-trusted@example.org", "trusted"), "trusted"},
		{"?restore_trust_level=true", "asked-blocked@example.org", card("asked-blocked@example.org", "blocked"), "blocked"},
	} {
		if rr := send(tc.query, "text/vcard", tc.file); rr.Code != http.StatusOK {
			t.Fatalf("%s %s: %d %s", tc.query, tc.email, rr.Code, rr.Body)
		}
		if got := trustOf(tc.email); got != tc.want {
			t.Errorf("import%s stored %s as %s, want %s", tc.query, tc.email, got, tc.want)
		}
	}
	// A default import is not refused for a level it does not read; one that restores is.
	if rr := send("", "text/vcard", card("default-owner@example.org", "owner")); rr.Code != http.StatusOK || trustOf("default-owner@example.org") != "auto" {
		t.Fatalf("default import of an unknown level: %d %s", rr.Code, rr.Body)
	}
	if rr := send("?restore_trust_level=true", "text/vcard", card("asked-owner@example.org", "owner")); rr.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(rr.Body.String(), `"cards[0].trust_level"`) {
		t.Fatalf("restoring an unknown level: %d %s", rr.Code, rr.Body)
	}
	if rr := send("?restore_trust_level=true", "application/json", `{"contacts":[{"email":"json@example.org"}]}`); rr.Code != http.StatusOK || trustOf("json@example.org") != "auto" {
		t.Fatalf("JSON import: %d %s", rr.Code, rr.Body)
	}
	// Only true or false is an answer; anything else is refused before the file is read.
	if rr := send("?restore_trust_level=yes", "text/vcard", card("yes@example.org", "trusted")); rr.Code < 400 || rr.Code >= 500 {
		t.Fatalf("restore_trust_level=yes: %d %s", rr.Code, rr.Body)
	}
	var stored int64
	if err := tx.Model(&models.Contact{}).Where("email IN ?", []string{"asked-owner@example.org", "yes@example.org"}).Count(&stored).Error; err != nil || stored != 0 {
		t.Fatalf("refused imports stored %d contacts (%v)", stored, err)
	}
}
