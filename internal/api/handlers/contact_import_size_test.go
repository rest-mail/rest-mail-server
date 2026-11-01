package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
)

// There is no limit on how many contacts one import holds: a file larger than one
// INSERT's batch all goes in, and the answer says what was already there (#1390).
func TestImportContactsTakesMoreThanOneBatch(t *testing.T) {
	db := openAuthzTestDB(t)
	if err := db.AutoMigrate(&models.Contact{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_contacts_mailbox_email ON contacts(mailbox_id, email)`).Error; err != nil {
		t.Fatal(err)
	}
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })
	fx := seedAuthzFixture(t, tx)
	if err := tx.Create(&models.Contact{MailboxID: fx.ownerMailbox.ID, Email: "person0@example.org", TrustLevel: "auto", Source: "manual"}).Error; err != nil {
		t.Fatal(err)
	}

	total := importBatchSize + 10
	entries := make([]string, total)
	for i := range entries {
		entries[i] = fmt.Sprintf(`{"email":"person%d@example.org"}`, i)
	}
	body := `{"contacts":[` + strings.Join(entries, ",") + `]}`

	router := chi.NewRouter()
	NewContactHandler(tx).RegisterContacts(endpoint.Routes{Router: router, Registry: endpoint.NewRegistry()})
	path := "/api/v1/accounts/" + uintToStr(fx.ownerMailbox.ID) + "/contacts/import"
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, authzRequest(http.MethodPost, path, fx.ownerMailbox.ID, nil, body))
	if rr.Code != http.StatusOK {
		t.Fatalf("%d %s", rr.Code, rr.Body)
	}
	var got struct {
		Data ContactsImported `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Data.Imported != total-1 || got.Data.Skipped != 1 {
		t.Fatalf("got %+v, want %d imported and 1 skipped", got.Data, total-1)
	}
}

// The file is read an entry at a time, and a body of the wrong shape gets the refusal a
// bound body would.
func TestEachImportEntryReadsTheFileAnEntryAtATime(t *testing.T) {
	var seen []string
	total, err := eachImportEntry(strings.NewReader(`{"contacts":[{"email":"a@example.org"},{"email":"nope"},{}]}`),
		func(i int, entry importContactEntry, problems map[string]string) error {
			seen = append(seen, fmt.Sprintf("%d %s %d", i, entry.Email, len(problems)))
			return nil
		})
	if err != nil || total != 3 {
		t.Fatalf("total=%d err=%v", total, err)
	}
	if want := "0 a@example.org 0|1  1|2  1"; strings.Join(seen, "|") != want {
		t.Fatalf("saw %q, want %q", strings.Join(seen, "|"), want)
	}
	// The request type's rules hold though the body never reaches the binder whole: the
	// contract run sends undeclared fields at both levels.
	_, entryProblems := readImportEntry(json.RawMessage(`{"email":"a@example.org","extra":1}`), 4)
	if len(entryProblems) == 0 {
		t.Fatal("an undeclared field in an entry was accepted")
	}
	for body, status := range map[string]int{
		`{`:                         http.StatusBadRequest,
		``:                          http.StatusBadRequest,
		`{"contacts":[]} extra`:     http.StatusBadRequest,
		`{"contacts":[{"email"`:     http.StatusBadRequest,
		`[]`:                        http.StatusUnprocessableEntity,
		`{"contacts":[],"extra":1}`: http.StatusUnprocessableEntity,
		`{"contacts":"x"}`:          http.StatusUnprocessableEntity,
	} {
		_, err := eachImportEntry(strings.NewReader(body), func(int, importContactEntry, map[string]string) error { return nil })
		var refusal *endpoint.Error
		if !errors.As(err, &refusal) || refusal.Status != status {
			t.Errorf("%q: %v, want %d", body, err, status)
		}
	}
}
