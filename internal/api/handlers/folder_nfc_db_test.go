package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/api/middleware"
	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
)

// The same name in both forms. Only the first is ever stored (#1669).
const (
	nfcCafe = "Caf\u00e9"
	nfdCafe = "Cafe\u0301"
)

func nameBody(name string) string { return fmt.Sprintf(`{"name":%q}`, name) }

// A folder name is saved in NFC, and a name that matches an existing folder once converted is
// refused like any other duplicate.
func TestCreateFolderSavesNFCAndRefusesAConvertedDuplicate(t *testing.T) {
	fx := newFolderRulesFixture(t)
	rr := fx.send(t, http.MethodPost, "", nameBody(nfdCafe))
	if rr.Code != http.StatusCreated || !strings.Contains(rr.Body.String(), nfcCafe) {
		t.Fatalf("create: %d %s, want the name in NFC", rr.Code, rr.Body)
	}
	if fx.count(t, nfcCafe) != 1 || fx.count(t, nfdCafe) != 0 {
		t.Fatalf("stored: %d in NFC, %d in NFD", fx.count(t, nfcCafe), fx.count(t, nfdCafe))
	}
	for _, spelling := range []string{nfdCafe, nfcCafe} {
		wantRefusal(t, fx.send(t, http.MethodPost, "", nameBody(spelling)), http.StatusConflict, "already_exists")
	}
	if fx.count(t, nfcCafe) != 1 {
		t.Fatalf("a refused create added a marker: %d", fx.count(t, nfcCafe))
	}
}

// RENAME saves its new name in NFC, refuses one taken after conversion, and finds its source from
// either spelling. DELETE finds its folder the same way.
func TestRenameAndDeleteFolderUseNFC(t *testing.T) {
	fx := newFolderRulesFixture(t)
	if rr := fx.send(t, http.MethodPatch, "Work", nameBody(nfdCafe)); rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), nfcCafe) {
		t.Fatalf("rename: %d %s", rr.Code, rr.Body)
	}
	if fx.count(t, nfcCafe) != 2 || fx.count(t, nfdCafe) != 0 {
		t.Fatalf("stored: %d in NFC, %d in NFD", fx.count(t, nfcCafe), fx.count(t, nfdCafe))
	}
	wantRefusal(t, fx.send(t, http.MethodPatch, "Personal", nameBody(nfdCafe)), http.StatusConflict, "already_exists")
	if fx.count(t, "Personal") != 1 {
		t.Fatal("a refused rename moved mail")
	}
	if rr := fx.send(t, http.MethodPatch, nfdCafe, nameBody("Tea")); rr.Code != http.StatusOK {
		t.Fatalf("rename from the other spelling: %d %s", rr.Code, rr.Body)
	}
	if fx.count(t, "Tea") != 2 {
		t.Fatalf("Tea holds %d", fx.count(t, "Tea"))
	}
	if rr := fx.send(t, http.MethodPatch, "Tea", nameBody(nfcCafe)); rr.Code != http.StatusOK {
		t.Fatalf("rename back: %d %s", rr.Code, rr.Body)
	}
	if rr := fx.send(t, http.MethodDelete, nfdCafe, ""); rr.Code != http.StatusNoContent && rr.Code != http.StatusOK {
		t.Fatalf("delete from the other spelling: %d %s", rr.Code, rr.Body)
	}
	if fx.count(t, nfcCafe) != 0 {
		t.Fatalf("%d messages survive the delete", fx.count(t, nfcCafe))
	}
}

// A client that sends the other spelling reads the folder's mail: from the folder's page and from
// search.
func TestFolderLookupsUseNFC(t *testing.T) {
	fx := newFolderRulesFixture(t)
	message := models.Message{MailboxID: fx.mailbox, Folder: nfcCafe, Sender: "a@b.test", Subject: "espresso", SizeBytes: 10}
	if err := fx.db.Create(&message).Error; err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := context.WithValue(r.Context(), middleware.ClaimsKey, &auth.Claims{AccountID: fx.mailbox})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	})
	routes := endpoint.Routes{Router: router, Registry: endpoint.NewRegistry()}
	NewMessageHandler(fx.db, nil, nil, "", nil).RegisterMessages(routes)
	NewSearchHandler(fx.db).Register(routes)

	rr := httptest.NewRecorder()
	path := accountFoldersPath(fx.mailbox) + "/" + url.PathEscape(nfdCafe) + "/messages"
	router.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "espresso") {
		t.Fatalf("folder page: %d %s", rr.Code, rr.Body)
	}

	rr = httptest.NewRecorder()
	path = fmt.Sprintf("/api/v1/accounts/%d/search?q=espresso&folder=%s", fx.mailbox, url.QueryEscape(nfdCafe))
	router.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "espresso") {
		t.Fatalf("search: %d %s", rr.Code, rr.Body)
	}
}
