package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/grants"
)

// A scheduled send is seen and called back with what sending it takes, mail:send as its
// from address (#743): a credential without it neither lists it nor cancels it.
func TestScheduledSends_NeedMailSendAsTheirSender(t *testing.T) {
	db := openScheduledSendTestDB(t)
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })

	fx := seedMsgDeleteFixture(t, tx, 0)
	other := models.Mailbox{DomainID: fx.mailbox.DomainID, LocalPart: "other", Address: "other@delq.test", Active: true, QuotaBytes: 1 << 30}
	if err := tx.Create(&other).Error; err != nil {
		t.Fatalf("create mailbox: %v", err)
	}
	schedule := func(from string) models.ScheduledSend {
		t.Helper()
		row := models.ScheduledSend{
			AccountID: fx.mailbox.ID, From: from, To: models.JSONB(`["rcpt@remote.test"]`), Subject: "later",
			SendAt: time.Now().Add(time.Hour), Status: "pending", Request: models.JSONB(`{}`),
		}
		if err := tx.Create(&row).Error; err != nil {
			t.Fatalf("create scheduled send: %v", err)
		}
		return row
	}
	own, foreign := schedule(fx.mailbox.Address), schedule(other.Address)

	h := NewMessageHandler(tx, nil, nil, "", nil)
	session := &auth.Claims{AccountID: fx.mailbox.ID}
	key := func(permissions ...auth.ScopedPermission) *auth.Claims {
		return &auth.Claims{AccountID: fx.mailbox.ID, CredentialID: 9, CredentialKind: auth.CredentialKindAPIKey, APIKeyPermissions: permissions}
	}
	list := func(claims *auth.Claims) []uint {
		t.Helper()
		rr := httptest.NewRecorder()
		composeRoutes(h).ServeHTTP(rr, withClaims(httptest.NewRequest(http.MethodGet, "/api/v1/messages/scheduled", nil), claims))
		if rr.Code != http.StatusOK {
			t.Fatalf("list: %d %s", rr.Code, rr.Body.String())
		}
		var listed struct {
			Data []ScheduledMessage `json:"data"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &listed); err != nil {
			t.Fatalf("decode list: %v", err)
		}
		ids := []uint{}
		for _, m := range listed.Data {
			ids = append(ids, m.ID)
		}
		return ids
	}
	cancel := func(claims *auth.Claims, row models.ScheduledSend) int {
		t.Helper()
		rr := httptest.NewRecorder()
		composeRoutes(h).ServeHTTP(rr, withClaims(httptest.NewRequest(http.MethodDelete, "/api/v1/messages/scheduled/"+uintToStr(row.ID), nil), claims))
		return rr.Code
	}
	status := func(row models.ScheduledSend) string {
		t.Helper()
		var reloaded models.ScheduledSend
		if err := tx.First(&reloaded, row.ID).Error; err != nil {
			t.Fatalf("reload: %v", err)
		}
		return reloaded.Status
	}
	same := func(got []uint, want ...uint) bool {
		if len(got) != len(want) {
			return false
		}
		for i := range got {
			if got[i] != want[i] {
				return false
			}
		}
		return true
	}

	// The session sends as its own mailbox, not as the other one it holds no grant on.
	if got := list(session); !same(got, own.ID) {
		t.Errorf("session lists %v, want only its own send %d", got, own.ID)
	}
	if code := cancel(session, foreign); code != http.StatusNotFound || status(foreign) != "pending" {
		t.Errorf("session cancelling a send as another mailbox = %d (%s), want 404 and still pending", code, status(foreign))
	}

	unrelated := key(auth.ScopedPermission{Capability: "queue:read", Scope: "*"})
	readOnly := key(auth.ScopedPermission{Capability: models.CapMailRead, Scope: grants.Mailbox(fx.mailbox.Address)})
	for name, claims := range map[string]*auth.Claims{"queue:read key": unrelated, "mail:read key": readOnly} {
		if got := list(claims); len(got) != 0 {
			t.Errorf("%s lists %v, want none", name, got)
		}
		if code := cancel(claims, own); code != http.StatusNotFound || status(own) != "pending" {
			t.Errorf("%s cancelling = %d (%s), want 404 and still pending", name, code, status(own))
		}
	}

	sender := key(auth.ScopedPermission{Capability: models.CapMailSend, Scope: grants.Mailbox(fx.mailbox.Address)})
	if got := list(sender); !same(got, own.ID) {
		t.Errorf("mail:send key lists %v, want %d", got, own.ID)
	}
	if code := cancel(sender, own); code != http.StatusNoContent || status(own) != "cancelled" {
		t.Errorf("mail:send key cancelling = %d (%s), want 204 and cancelled", code, status(own))
	}
}
