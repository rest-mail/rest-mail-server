package handlers

import (
	"mime"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/api/middleware"
	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/grants"
)

func TestContactVCardExportDB(t *testing.T) {
	db := openAuthzTestDB(t)
	if err := migrateModels(db, &models.ServerLimits{}, &models.Contact{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_contacts_mailbox_email ON contacts(mailbox_id, email)`).Error; err != nil {
		t.Fatal(err)
	}
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	fx := seedAuthzFixture(t, tx)
	router := contactRoutes(NewContactHandler(tx))
	owner, other := fx.ownerMailbox, fx.attackerMailbox
	export := func(caller, account models.Mailbox, permissions ...auth.ScopedPermission) *httptest.ResponseRecorder {
		request := authzRequest(http.MethodGet, contactsPathOf(account.ID, "/export.vcf"), caller.ID, nil, "")
		if len(permissions) > 0 {
			claims := middleware.GetClaims(request)
			claims.CredentialKind = auth.CredentialKindAPIKey
			claims.APIKeyPermissions = permissions
		}
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		return recorder
	}
	checkDownload := func(t *testing.T, rr *httptest.ResponseRecorder) {
		t.Helper()
		if rr.Code != http.StatusOK || !rr.Flushed || rr.Header().Get("Content-Type") != "text/vcard; charset=utf-8" || rr.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("download: %d (flushed %t), %v, %s", rr.Code, rr.Flushed, rr.Header(), rr.Body)
		}
		disposition, params, err := mime.ParseMediaType(rr.Header().Get("Content-Disposition"))
		if err != nil || disposition != "attachment" || params["filename"] != "contacts.vcf" {
			t.Fatalf("download disposition: %q %v (%v)", disposition, params, err)
		}
	}

	t.Run("empty", func(t *testing.T) {
		rr := export(owner, owner)
		checkDownload(t, rr)
		if rr.Body.Len() != 0 {
			t.Fatalf("empty address book contains %q", rr.Body)
		}
	})

	t.Run("stored fields round trip", func(t *testing.T) {
		const card = "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Élodie\\, Example\\; Dr.\\nResearch\r\n" +
			"EMAIL;TYPE=work:work@example.test\r\nEMAIL;TYPE=home:home@example.test\r\n" +
			"X-RESTMAIL-TRUST-LEVEL:trusted\r\nX-RESTMAIL-SOURCE:manual\r\n" +
			"X-RESTMAIL-CREATED-AT:2026-01-02T03:04:05.123456Z\r\nX-RESTMAIL-UPDATED-AT:2026-02-03T04:05:06.654321Z\r\nEND:VCARD\r\n"
		request := authzRequest(http.MethodPost, contactsPathOf(owner.ID, "/import?restore_trust_level=true"), owner.ID, nil, card)
		request.Header.Set("Content-Type", "text/vcard")
		imported := httptest.NewRecorder()
		router.ServeHTTP(imported, request)
		if imported.Code != http.StatusOK {
			t.Fatalf("import: %d %s", imported.Code, imported.Body)
		}

		rr := export(owner, owner, auth.ScopedPermission{Capability: models.CapMailRead, Scope: grants.Mailbox(owner.Address)})
		checkDownload(t, rr)
		contacts, problems, total, err := readVCardFileRestoring(t, rr.Body.String(), true)
		if err != nil || len(problems) != 0 || total != 2 || len(contacts) != 2 {
			t.Fatalf("round trip: %d cards, %+v, problems %v, error %v", total, contacts, problems, err)
		}
		createdAt := time.Date(2026, 1, 2, 3, 4, 5, 123456000, time.UTC)
		updatedAt := time.Date(2026, 2, 3, 4, 5, 6, 654321000, time.UTC)
		for i, email := range []string{"home@example.test", "work@example.test"} {
			contact := contacts[i]
			if contact.Email != email || contact.Name != "Élodie, Example; Dr.\nResearch" || contact.TrustLevel != "trusted" || contact.Source != "manual" ||
				!contact.CreatedAt.Equal(createdAt) || !contact.UpdatedAt.Equal(updatedAt) {
				t.Errorf("contact %d: %+v", i, contact)
			}
		}
		if count := strings.Count(rr.Body.String(), "\r\nUID:urn:uuid:"); count != 2 {
			t.Errorf("expected two vCards with URI identifiers, got %d", count)
		}
	})

	t.Run("account and credential scope", func(t *testing.T) {
		const foreignEmail = "private-to-other@example.test"
		if err := tx.Create(&models.Contact{MailboxID: other.ID, Email: foreignEmail}).Error; err != nil {
			t.Fatal(err)
		}
		otherBook := export(other, other)
		checkDownload(t, otherBook)
		if !strings.Contains(otherBook.Body.String(), foreignEmail) {
			t.Fatalf("missing other account's own contact: %s", otherBook.Body)
		}
		ownBook := export(owner, owner)
		checkDownload(t, ownBook)
		if strings.Contains(ownBook.Body.String(), foreignEmail) {
			t.Fatalf("foreign contact in owner export: %s", ownBook.Body)
		}
		foreign := export(owner, other)
		missing := export(owner, models.Mailbox{ID: 2147483647})
		if foreign.Code != http.StatusNotFound || missing.Code != foreign.Code || foreign.Body.String() != missing.Body.String() || foreign.Header().Get("Content-Disposition") != "" {
			t.Fatalf("foreign export %d %s differs from missing %d %s", foreign.Code, foreign.Body, missing.Code, missing.Body)
		}
		writeOnly := export(owner, owner, auth.ScopedPermission{Capability: models.CapMailWrite, Scope: grants.Mailbox(owner.Address)})
		if writeOnly.Code != http.StatusForbidden || writeOnly.Header().Get("Content-Disposition") != "" {
			t.Fatalf("write-only export: %d %s", writeOnly.Code, writeOnly.Body)
		}
	})
}
