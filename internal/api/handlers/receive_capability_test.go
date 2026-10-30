package handlers

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/bootstrap"
	rmdb "github.com/restmail/restmail/internal/db"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/grants"
	"github.com/restmail/restmail/internal/pipeline"
	"github.com/restmail/restmail/internal/pipeline/filters"
	"gorm.io/gorm"
)

// Fixtures that create mailbox rows directly must also create the normal
// receive grants that account creation now persists.
func seedReceiveFixture(t *testing.T, db *gorm.DB, mailboxes ...models.Mailbox) {
	t.Helper()
	if err := db.AutoMigrate(&models.Mailbox{}, &models.Role{}, &models.Capability{}, &models.RoleCapability{}, &models.Grant{}, &models.GrantActivation{}); err != nil {
		t.Fatal(err)
	}
	if err := rmdb.MigrateAuditLog(db); err != nil {
		t.Fatal(err)
	}
	if err := bootstrap.EnsureRBAC(db); err != nil {
		t.Fatal(err)
	}
	for i := range mailboxes {
		if err := grants.EnsureReceive(db, &mailboxes[i]); err != nil {
			t.Fatal(err)
		}
	}
}

// The storage/policy fault drivers have receive-capable fixture accounts and
// server ceilings. Their scripted steps continue to describe the dependency
// whose failure the test exercises; authorization failures have separate tests.
func receiveFixtureRows(query string) (driver.Rows, bool) {
	if strings.Contains(query, "FROM grants AS g") {
		return &deliveryReadRows{columns: []string{"scope", "capability"}, values: []driver.Value{"*", models.CapMailReceive}}, true
	}
	if strings.Contains(query, `SELECT "address","owner_id" FROM "mailboxes"`) {
		return &deliveryReadRows{columns: []string{"address", "owner_id"}}, true
	}
	return nil, false
}

// Receiving is a grant, independent of sending, and every inbound entry point
// hides an account without it exactly as it hides an unknown recipient.
func TestReceiveCapabilityInbound(t *testing.T) {
	db := openAccountsTestDB(t)
	if err := db.AutoMigrate(&models.Message{}, &models.Pipeline{}, &models.Alias{}, &models.SieveScript{}, &models.VacationConfig{}, &models.Contact{}); err != nil {
		t.Fatal(err)
	}
	fx := seedAccountsFixture(t, db)
	t.Cleanup(func() {
		db.Where("mailbox_id IN (?)", db.Model(&models.Mailbox{}).Select("id").Where("domain_id = ?", fx.domain.ID)).Delete(&models.Message{})
		db.Where("domain_id = ?", fx.domain.ID).Delete(&models.Alias{})
		db.Where("domain_id = ?", fx.domain.ID).Delete(&models.Pipeline{})
	})
	if err := db.Model(&models.Domain{}).Where("id = ?", fx.domain.ID).Update("active", true).Error; err != nil {
		t.Fatal(err)
	}
	cache := grants.NewCache(db, grants.CacheTTL)
	accounts := NewAccountHandler(db, cache, "")
	claims := &auth.Claims{AccountID: fx.admin.ID, AMR: []string{auth.AMRPassword, auth.AMROTP}, TwoFactor: true}
	create := func(local, option string) accountResponse {
		t.Helper()
		body := fmt.Sprintf(`{"address":%q,"owned":true%s}`, local+"@"+fx.domain.Name, option)
		r := httptest.NewRequest(http.MethodPost, "/api/v1/accounts", strings.NewReader(body))
		r = r.WithContext(context.WithValue(r.Context(), middleware.ClaimsKey, claims))
		w := httptest.NewRecorder()
		accountRoutes(accounts).ServeHTTP(w, r)
		if w.Code != http.StatusCreated {
			t.Fatalf("create: %d %s", w.Code, w.Body)
		}
		return decodeAccount(t, w)
	}
	ordinary := create("ordinary", "")
	enabled := create("enabled", `,"receive":true`)
	disabled := create("disabled", `,"receive":false`)
	// An explicit alias cannot redirect a known nonreceiving identity.
	if err := db.Create(&models.Alias{DomainID: fx.domain.ID, SourceAddress: disabled.Address, DestinationAddress: enabled.Address, Active: true}).Error; err != nil {
		t.Fatal(err)
	}
	mailboxes := &MailboxHandler{db: db}
	rest := NewRestmailHandler(db, nil, nil, RestmailTarpitConfig{}, RestmailDeliverAuthConfig{})
	for _, tc := range []struct {
		account accountResponse
		accepts bool
	}{{ordinary, true}, {enabled, true}, {disabled, false}} {
		t.Run(tc.account.Address, func(t *testing.T) {
			held, err := grants.Load(db, tc.account.ID, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if got := held.Allows("mail:receive", grants.Mailbox(tc.account.Address)); got != tc.accepts {
				t.Fatalf("receive = %v, want %v", got, tc.accepts)
			}
			if !held.Allows(models.CapMailSend, grants.Mailbox(tc.account.Address)) {
				t.Fatal("receive option changed sending")
			}
			w := httptest.NewRecorder()
			addressCheckRoute(mailboxes)(w, httptest.NewRequest(http.MethodGet, "/api/mailboxes?address="+url.QueryEscape(tc.account.Address), nil))
			want := fmt.Sprintf(`"exists":%t`, tc.accepts)
			if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), want) {
				t.Fatalf("SMTP recipient check: %d %s", w.Code, w.Body)
			}
			_, _, err = rest.resolveRecipientMailbox(t.Context(), tc.account.Address)
			if (err == nil) != tc.accepts {
				t.Fatalf("RESTMAIL recipient check: %v", err)
			}
			factory := filters.NewRecipientCheck(db)
			filter, err := factory(nil)
			if err != nil {
				t.Fatal(err)
			}
			result, err := filter.Execute(t.Context(), &pipeline.EmailJSON{Envelope: pipeline.Envelope{RcptTo: []string{tc.account.Address}}})
			if err != nil || (result.Action == pipeline.ActionContinue) != tc.accepts {
				t.Fatalf("recipient filter: %+v %v", result, err)
			}
		})
	}
	unknown := "unknown@" + fx.domain.Name
	delivery := deliveryRoute(NewMessageHandler(db, nil, nil, "", nil))
	for _, account := range []accountResponse{ordinary, enabled} {
		w := httptest.NewRecorder()
		delivery(w, httptest.NewRequest(http.MethodPost, "/api/v1/messages/deliver", strings.NewReader(fmt.Sprintf(`{"address":%q,"sender":"sender@remote.test","subject":"Hi"}`, account.Address))))
		if w.Code != http.StatusCreated {
			t.Fatalf("REST receive-capable delivery: %d %s", w.Code, w.Body)
		}
		w = httptest.NewRecorder()
		restmailRoutes(rest).ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/restmail/messages", strings.NewReader(fmt.Sprintf(`{"from":"sender@remote.test","to":[%q],"subject":"Hi"}`, account.Address))))
		if w.Code != http.StatusCreated {
			t.Fatalf("RESTMAIL receive-capable delivery: %d %s", w.Code, w.Body)
		}
	}
	for _, body := range []string{fmt.Sprintf(`{"address":%q}`, disabled.Address), fmt.Sprintf(`{"mailbox_id":%d}`, disabled.ID)} {
		w := httptest.NewRecorder()
		delivery(w, httptest.NewRequest(http.MethodPost, "/api/v1/messages/deliver", strings.NewReader(body)))
		missing := httptest.NewRecorder()
		delivery(missing, httptest.NewRequest(http.MethodPost, "/api/v1/messages/deliver", strings.NewReader(fmt.Sprintf(`{"address":%q}`, unknown))))
		if w.Code != http.StatusNotFound || w.Body.String() != missing.Body.String() {
			t.Fatalf("REST refusal: %d %s; unknown: %d %s", w.Code, w.Body, missing.Code, missing.Body)
		}
	}
	for _, address := range []string{disabled.Address, unknown} {
		w := httptest.NewRecorder()
		restmailRoutes(rest).ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/restmail/messages", strings.NewReader(fmt.Sprintf(`{"from":"sender@remote.test","to":[%q],"subject":"Hi"}`, address))))
		if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), `"undeliverable"`) {
			t.Fatalf("RESTMAIL refusal: %d %s", w.Code, w.Body)
		}
	}
	// A configured filter cannot override the gate, including when an alias
	// claims the disabled exact identity or the recipient has a plus tag.
	factory := filters.NewRecipientCheck(db)
	filter, err := factory(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, address := range []string{disabled.Address, strings.Replace(disabled.Address, "@", "+tag@", 1), unknown} {
		result, err := filter.Execute(t.Context(), &pipeline.EmailJSON{Envelope: pipeline.Envelope{RcptTo: []string{address}}})
		if err != nil || result.Action != pipeline.ActionReject || result.RejectMsg != fmt.Sprintf("550 5.1.1 <%s>: Recipient not found", address) {
			t.Fatalf("pipeline refusal: %+v %v", result, err)
		}
	}
	discard := &receiveDiscardFilter{}
	registry := pipeline.NewRegistry()
	registry.Register(discard.Name(), func([]byte) (pipeline.Filter, error) { return discard, nil })
	engine := pipeline.NewEngine(registry, nil)
	if err := db.Create(&models.Pipeline{DomainID: fx.domain.ID, Direction: "inbound", Active: true, Filters: json.RawMessage(`[{"name":"receive_discard","type":"action","enabled":true}]`)}).Error; err != nil {
		t.Fatal(err)
	}
	restFiltered := NewRestmailHandler(db, engine, nil, RestmailTarpitConfig{}, RestmailDeliverAuthConfig{})
	refusal := httptest.NewRecorder()
	restmailRoutes(restFiltered).ServeHTTP(refusal, httptest.NewRequest(http.MethodPost, "/restmail/messages", strings.NewReader(fmt.Sprintf(`{"from":"sender@remote.test","to":[%q]}`, disabled.Address))))
	if refusal.Code != http.StatusUnprocessableEntity || len(discard.seen) != 0 {
		t.Fatalf("filter overrode receive gate: %d %s; executions=%d", refusal.Code, refusal.Body, len(discard.seen))
	}
	mixed := httptest.NewRecorder()
	restmailRoutes(restFiltered).ServeHTTP(mixed, httptest.NewRequest(http.MethodPost, "/restmail/messages", strings.NewReader(fmt.Sprintf(`{"from":"sender@remote.test","to":[%q,%q,%q]}`, disabled.Address, enabled.Address, unknown))))
	var mixedResponse struct {
		Data RestmailDelivery `json:"data"`
	}
	if err := json.Unmarshal(mixed.Body.Bytes(), &mixedResponse); err != nil {
		t.Fatal(err)
	}
	if mixed.Code != http.StatusCreated || len(mixedResponse.Data.Delivered) != 1 || mixedResponse.Data.Delivered[0] != enabled.Address || len(mixedResponse.Data.Failed) != 2 || len(discard.seen) != 1 || len(discard.seen[0]) != 1 || discard.seen[0][0] != enabled.Address {
		t.Fatalf("mixed receive/filter result: %d %s; envelopes=%v", mixed.Code, mixed.Body, discard.seen)
	}
	grantHandler := grantRoutes(NewGrantHandler(db, cache, ""))
	change := func(method, path, body string, who *auth.Claims) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		grantHandler.ServeHTTP(w, withClaims(httptest.NewRequest(method, path, strings.NewReader(body)), who))
		return w
	}
	grantBody := fmt.Sprintf(`{"holder":{"kind":"account","id":%d},"role":"receiver","scope":%q}`, disabled.ID, grants.Mailbox(disabled.Address))
	passwordOnly := &auth.Claims{AccountID: fx.admin.ID, AMR: []string{auth.AMRPassword}}
	if w := change(http.MethodPost, "/api/v1/admin/grants", grantBody, passwordOnly); w.Code != http.StatusForbidden {
		t.Fatalf("grant without 2FA: %d %s", w.Code, w.Body)
	}
	unrelated := &auth.Claims{AccountID: fx.user.ID, AMR: []string{auth.AMRPassword, auth.AMROTP}, TwoFactor: true}
	if w := change(http.MethodPost, "/api/v1/admin/grants", grantBody, unrelated); w.Code != http.StatusForbidden {
		t.Fatalf("grant beyond held: %d %s", w.Code, w.Body)
	}
	w := change(http.MethodPost, "/api/v1/admin/grants", grantBody, claims)
	if w.Code != http.StatusCreated {
		t.Fatalf("grant: %d %s", w.Code, w.Body)
	}
	var response struct {
		Data grantResponse `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	var mailbox models.Mailbox
	if err := db.First(&mailbox, disabled.ID).Error; err != nil {
		t.Fatal(err)
	}
	if allowed, err := grants.CanReceive(db, &mailbox); err != nil || !allowed {
		t.Fatalf("grant did not enable receive: %v %v", allowed, err)
	}
	// Owning an account is a live ceiling. Its own receive grant must not
	// outlive the owner's access on that mailbox.
	expired := time.Now().Add(-time.Minute)
	ownerGrant := db.Model(&models.Grant{}).Where("holder_kind = ? AND holder_id = ? AND scope = ? AND role_id = (SELECT id FROM roles WHERE name = ?)", models.GrantHolderAccount, fx.admin.ID, grants.Mailbox(disabled.Address), models.RoleOwner)
	if err := ownerGrant.Update("expires_at", expired).Error; err != nil {
		t.Fatal(err)
	}
	if allowed, err := grants.CanReceive(db, &mailbox); err != nil || allowed {
		t.Fatalf("receive escaped live owner ceiling: %v %v", allowed, err)
	}
	if err := ownerGrant.Update("expires_at", nil).Error; err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/v1/admin/grants/%d", response.Data.ID)
	if w := change(http.MethodDelete, path, "", passwordOnly); w.Code != http.StatusForbidden {
		t.Fatalf("remove without 2FA: %d %s", w.Code, w.Body)
	}
	if w := change(http.MethodDelete, path, "", claims); w.Code != http.StatusNoContent {
		t.Fatalf("remove: %d %s", w.Code, w.Body)
	}
	if allowed, err := grants.CanReceive(db, &mailbox); err != nil || allowed {
		t.Fatalf("revoked receive still held: %v %v", allowed, err)
	}
	for _, action := range []string{grants.ActionCreate, grants.ActionDelete} {
		var entries []models.AuditEntry
		if err := db.Where("action = ? AND target->>'grant_id' = ?", action, fmt.Sprint(response.Data.ID)).Find(&entries).Error; err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 || entries[0].ActorID == nil || *entries[0].ActorID != fx.admin.ID {
			t.Fatalf("grant audit %s: %+v", action, entries)
		}
	}
}

type receiveDiscardFilter struct{ seen [][]string }

func (*receiveDiscardFilter) Name() string              { return "receive_discard" }
func (*receiveDiscardFilter) Type() pipeline.FilterType { return pipeline.FilterTypeAction }
func (f *receiveDiscardFilter) Execute(_ context.Context, email *pipeline.EmailJSON) (*pipeline.FilterResult, error) {
	f.seen = append(f.seen, append([]string(nil), email.Envelope.RcptTo...))
	return &pipeline.FilterResult{Type: pipeline.FilterTypeAction, Action: pipeline.ActionDiscard}, nil
}

func TestReceivePermissionReadFailureDefersBeforeFilters(t *testing.T) {
	for _, gateway := range []bool{false, true} {
		d := &deliveryReadDB{receiveErr: errors.New("private receive grant storage failure"), steps: []deliveryReadStep{deliveryDomainStep(), deliveryMailboxStep("one@example.test")}}
		path, body, status := restmailMessagesPath, `{"from":"sender@external.test","to":["one@example.test"]}`, http.StatusServiceUnavailable
		route := restmailRoutes(NewRestmailHandler(deliveryReadGorm(t, d), nil, nil, RestmailTarpitConfig{}, RestmailDeliverAuthConfig{TrustedCIDRs: []string{"192.0.2.10/32"}}))
		if gateway {
			d.steps = d.steps[1:]
			path, body, status = "/api/v1/messages/deliver", `{"address":"one@example.test"}`, http.StatusInternalServerError
			route = gatewayFaultRoutes(deliveryReadGorm(t, d))
		}
		w := httptest.NewRecorder()
		route.ServeHTTP(w, deliveryRequest(t, body, path))
		if w.Code != status || d.writes != 0 || strings.Contains(w.Body.String(), "private") {
			t.Fatalf("receive grant failure: %d %s writes=%d", w.Code, w.Body, d.writes)
		}
	}
}
