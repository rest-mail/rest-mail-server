package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/grants"
)

// subscribedNames is the account's subscription list as listSubscriptions answers it.
func (f *credentialFixture) subscribedNames(authorization string) []string {
	f.t.Helper()
	rr := f.do(http.MethodGet, fmt.Sprintf("/api/v1/accounts/%d/subscriptions", f.mailboxID), authorization, "")
	if rr.Code != http.StatusOK {
		f.t.Fatalf("list subscriptions: %d %s", rr.Code, rr.Body)
	}
	var list struct {
		Data []struct {
			Name string `json:"name"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		f.t.Fatal(err)
	}
	names := []string{}
	for _, d := range list.Data {
		names = append(names, d.Name)
	}
	return names
}

// The account's IMAP subscription list (#1675), against Postgres: a new account
// starts on its standard folders, SUBSCRIBE and UNSUBSCRIBE are idempotent, and
// a name outlives its folder's DELETE and RENAME (RFC 9051 §§6.3.6-6.3.7).
func TestFolderSubscriptionsAgainstPostgres(t *testing.T) {
	f := newCredentialFixture(t)
	standard := []string{"Archive", "Drafts", "INBOX", "Sent", "Spam", "Trash"}
	var rows int64
	if err := f.db.Model(&models.FolderSubscription{}).Where("mailbox_id = ?", f.mailboxID).Count(&rows).Error; err != nil || rows != 0 {
		t.Fatalf("a new account stored %d subscription rows (%v), want none: the standard folders need none", rows, err)
	}
	if got := f.subscribedNames(f.session); !slices.Equal(got, standard) {
		t.Fatalf("new account's subscriptions = %q, want %q", got, standard)
	}
	sub := func(method, folder string, want int) {
		t.Helper()
		rr := f.do(method, fmt.Sprintf("/api/v1/accounts/%d/subscriptions/%s", f.mailboxID, folder), f.session, "")
		if rr.Code != want {
			t.Fatalf("%s %s: %d %s, want %d", method, folder, rr.Code, rr.Body, want)
		}
	}
	sub(http.MethodPut, "Projects%2F2026", http.StatusOK)
	sub(http.MethodPut, "Projects%2F2026", http.StatusOK) // already subscribed
	sub(http.MethodPut, "inbox", http.StatusOK)           // INBOX in any case is INBOX
	sub(http.MethodPut, "a..b", http.StatusUnprocessableEntity)
	sub(http.MethodDelete, "Spam", http.StatusNoContent)
	sub(http.MethodDelete, "Spam", http.StatusNoContent) // not subscribed
	want := []string{"Archive", "Drafts", "INBOX", "Projects/2026", "Sent", "Trash"}
	if got := f.subscribedNames(f.session); !slices.Equal(got, want) {
		t.Fatalf("after SUBSCRIBE/UNSUBSCRIBE = %q, want %q", got, want)
	}
	sub(http.MethodPut, "Spam", http.StatusOK) // a standard folder comes back
	sub(http.MethodDelete, "Spam", http.StatusNoContent)
	sub(http.MethodPut, "Scratch", http.StatusOK)
	sub(http.MethodDelete, "Scratch", http.StatusNoContent)
	if got := f.subscribedNames(f.session); !slices.Equal(got, want) {
		t.Fatalf("after re-SUBSCRIBE and UNSUBSCRIBE = %q, want %q", got, want)
	}
	// Projects/2026 and INBOX subscribed, Spam unsubscribed; Scratch left no row.
	if err := f.db.Model(&models.FolderSubscription{}).Where("mailbox_id = ?", f.mailboxID).Count(&rows).Error; err != nil || rows != 3 {
		t.Fatalf("stored rows = %d (%v), want 3", rows, err)
	}

	folders := fmt.Sprintf("/api/v1/accounts/%d/folders", f.mailboxID)
	for _, name := range []string{"Old", "Gone"} {
		if rr := f.do(http.MethodPost, folders, f.session, fmt.Sprintf(`{"name":%q}`, name)); rr.Code != http.StatusCreated {
			t.Fatalf("create %s: %d %s", name, rr.Code, rr.Body)
		}
		sub(http.MethodPut, name, http.StatusOK)
	}
	if rr := f.do(http.MethodPatch, folders+"/Old", f.session, `{"name":"New"}`); rr.Code != http.StatusOK {
		t.Fatalf("rename: %d %s", rr.Code, rr.Body)
	}
	if rr := f.do(http.MethodDelete, folders+"/Gone", f.session, ""); rr.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rr.Code, rr.Body)
	}
	want = []string{"Archive", "Drafts", "Gone", "INBOX", "Old", "Projects/2026", "Sent", "Trash"}
	if got := f.subscribedNames(f.session); !slices.Equal(got, want) {
		t.Fatalf("after RENAME Old New and DELETE Gone = %q, want %q", got, want)
	}

	// The account's own mail:read lists and mail:write changes, as the folder routes have it.
	key, public, hash, err := auth.NewAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	reader := models.Credential{AccountID: f.accountID, Kind: models.CredentialKindAPIKey, Name: "reader", PublicID: public, SecretHash: hash,
		Scope: models.CredentialScope{Permissions: []auth.ScopedPermission{{Capability: models.CapMailRead, Scope: grants.Mailbox(f.address)}}}}
	if err := f.db.Create(&reader).Error; err != nil {
		t.Fatal(err)
	}
	if got := f.subscribedNames("Bearer " + key); !slices.Equal(got, want) {
		t.Fatalf("mail:read key lists %q", got)
	}
	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		rr := f.do(method, fmt.Sprintf("/api/v1/accounts/%d/subscriptions/Gone", f.mailboxID), "Bearer "+key, "")
		if rr.Code != http.StatusForbidden {
			t.Fatalf("mail:read key %s: %d %s, want 403", method, rr.Code, rr.Body)
		}
	}

	// Another account's list is not reachable, and a deleted mailbox takes its list with it.
	other := f.seedMailbox(f.mustDomainID(), "dana")
	if rr := f.do(http.MethodGet, fmt.Sprintf("/api/v1/accounts/%d/subscriptions", other), f.session, ""); rr.Code != http.StatusNotFound {
		t.Fatalf("another account's list: %d %s, want 404", rr.Code, rr.Body)
	}
	if err := f.db.Omit("Mailbox").Create(&models.FolderSubscription{MailboxID: other, Folder: "Spam"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Delete(&models.Mailbox{}, other).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&models.FolderSubscription{}).Where("mailbox_id = ?", other).Count(&rows).Error; err != nil || rows != 0 {
		t.Fatalf("deleted mailbox kept %d subscriptions (%v)", rows, err)
	}
}

func (f *credentialFixture) mustDomainID() uint {
	f.t.Helper()
	var box models.Mailbox
	if err := f.db.First(&box, f.mailboxID).Error; err != nil {
		f.t.Fatal(err)
	}
	return box.DomainID
}
