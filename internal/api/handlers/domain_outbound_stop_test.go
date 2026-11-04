package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/api/middleware"
	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/grants"
)

// An operator stops one domain's outbound mail with a reason and restarts it
// (#1590). Only queue:manage at the server's own scope may: a domain's
// administrator, even one granted queue:manage on that domain, is refused. Each
// change is audited with who and why, the domain answers with the stop, inbound
// mail to the stopped domain is delivered as before, and a restart gives the held
// mail back the queue lifetime it spent held.
func TestDomainOutboundStop_ThroughTheAdminAPI(t *testing.T) {
	db := openMsgDeleteTestDB(t)
	if err := db.AutoMigrate(&models.QuotaUsage{}, &models.AuditEntry{}, &models.OutboundQueue{}); err != nil {
		t.Skipf("outbound stop DB test skipped: migrate failed (%v)", err)
	}
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })

	fx := seedMsgDeleteFixture(t, tx, 0)
	var domain models.Domain
	if err := tx.First(&domain, fx.mailbox.DomainID).Error; err != nil {
		t.Fatal(err)
	}
	queued := models.OutboundQueue{Sender: fx.mailbox.Address, Recipient: "someone@remote.test", Domain: "remote.test",
		RawMessage: "Subject: hi\r\n\r\nhi\r\n", Status: "deferred", ExpiresAt: time.Now().Add(time.Hour)}
	if err := tx.Create(&queued).Error; err != nil {
		t.Fatal(err)
	}
	// Queued two days ago, before the stop: it was held for all of the stop.
	if err := tx.Model(&queued).Update("created_at", time.Now().Add(-48*time.Hour)).Error; err != nil {
		t.Fatal(err)
	}
	fresh := models.OutboundQueue{Sender: fx.mailbox.Address, Recipient: "other@remote.test", Domain: "remote.test",
		RawMessage: "Subject: hi\r\n\r\nhi\r\n", Status: "pending", ExpiresAt: time.Now().Add(time.Hour)}

	const operator, domainAdmin uint = 9001, 9002
	held := func(account uint) (grants.Held, error) {
		if account == operator {
			return grants.HeldOf(grants.Entry{Scope: grants.All, Capability: middleware.CapQueueManage}), nil
		}
		return grants.HeldOf(
			grants.Entry{Scope: grants.Domain(domain.Name), Capability: middleware.CapDomainsWrite},
			grants.Entry{Scope: grants.Domain(domain.Name), Capability: middleware.CapQueueManage},
		), nil
	}
	check := func(scope middleware.ScopeOf) func(string) endpoint.Middleware {
		return func(capability string) endpoint.Middleware {
			return endpoint.Middleware{Wrap: middleware.RequireCapability(held, capability, scope)}
		}
	}
	routes := chi.NewRouter()
	NewDomainHandler(tx, nil).Register(endpoint.Routes{Router: routes, Registry: endpoint.NewRegistry()},
		DomainRouteChecks{At: check(nil), OnList: check(nil), OnDomain: check(DomainScopeOf(tx))})

	call := func(account uint, method, body string) (int, Domain) {
		t.Helper()
		req := httptest.NewRequest(method, fmt.Sprintf("/api/v1/admin/domains/%d/outbound-stop", domain.ID), strings.NewReader(body))
		address := fmt.Sprintf("admin-%d@example.test", account)
		req = req.WithContext(context.WithValue(req.Context(), middleware.ClaimsKey, &auth.Claims{AccountID: account, Address: address}))
		rr := httptest.NewRecorder()
		routes.ServeHTTP(rr, req)
		var answer struct {
			Data Domain `json:"data"`
		}
		if rr.Code == http.StatusOK {
			if err := json.Unmarshal(rr.Body.Bytes(), &answer); err != nil {
				t.Fatalf("decode %s: %v", rr.Body, err)
			}
		}
		return rr.Code, answer.Data
	}
	audited := func(action string) []models.AuditEntry {
		t.Helper()
		var entries []models.AuditEntry
		if err := tx.Where("action = ? AND target->>'domain_id' = ?", action, fmt.Sprint(domain.ID)).Order("seq").Find(&entries).Error; err != nil {
			t.Fatal(err)
		}
		return entries
	}
	stopped := func() bool {
		t.Helper()
		var d models.Domain
		if err := tx.First(&d, domain.ID).Error; err != nil {
			t.Fatal(err)
		}
		return d.OutboundStoppedAt != nil
	}

	if code, _ := call(domainAdmin, http.MethodPut, `{"reason":"mine"}`); code != http.StatusForbidden || stopped() {
		t.Fatalf("the domain's administrator stopping it: %d, stopped %v; want 403 and not stopped", code, stopped())
	}
	if code, _ := call(operator, http.MethodPut, `{"reason":"  "}`); code != http.StatusUnprocessableEntity || stopped() {
		t.Fatalf("a stop without a reason: %d; want 422 and not stopped", code)
	}

	code, answer := call(operator, http.MethodPut, `{"reason":"complaint spike from a compromised mailbox"}`)
	if code != http.StatusOK || answer.OutboundStop == nil || answer.OutboundStop.Reason != "complaint spike from a compromised mailbox" ||
		answer.OutboundStop.StoppedBy != fmt.Sprintf("admin-%d@example.test", operator) || !stopped() {
		t.Fatalf("the operator's stop: %d, answered %+v; want 200 with the stop, its reason and who made it", code, answer.OutboundStop)
	}
	if e := audited("domain.outbound_stop"); len(e) != 1 || e[0].ActorName != fmt.Sprintf("admin-%d@example.test", operator) ||
		e[0].Target["reason"] != "complaint spike from a compromised mailbox" || e[0].OccurredAt.IsZero() {
		t.Fatalf("the stop's audit entries: %+v; want one with who, when and why", e)
	}

	// Sent while stopped: it was held for less than the stop's age.
	if err := tx.Create(&fresh).Error; err != nil {
		t.Fatal(err)
	}

	// Inbound mail to the stopped domain is delivered as before.
	rr := httptest.NewRecorder()
	deliveryRoute(NewMessageHandler(tx, nil, nil, "", nil))(rr, httptest.NewRequest(http.MethodPost, "/api/v1/messages/deliver",
		strings.NewReader(fmt.Sprintf(`{"mailbox_id":%d,"sender":"someone@remote.test","subject":"still arrives","body_text":"hello"}`, fx.mailbox.ID))))
	if rr.Code != http.StatusCreated || countMessages(t, tx, fx.mailbox.ID) != 1 {
		t.Fatalf("inbound delivery to a stopped domain: %d %s, %d messages; want 201 and the message stored", rr.Code, rr.Body, countMessages(t, tx, fx.mailbox.ID))
	}

	if code, _ := call(domainAdmin, http.MethodDelete, ""); code != http.StatusForbidden || !stopped() {
		t.Fatalf("the domain's administrator restarting it: %d; want 403 and still stopped", code)
	}
	// Pretend the stop is a day old, so the restart has a hold to give back.
	if err := tx.Model(&models.Domain{}).Where("id = ?", domain.ID).Update("outbound_stopped_at", time.Now().Add(-24*time.Hour)).Error; err != nil {
		t.Fatal(err)
	}
	code, answer = call(operator, http.MethodDelete, "")
	if code != http.StatusOK || answer.OutboundStop != nil || stopped() {
		t.Fatalf("the operator's restart: %d, answered %+v; want 200 and no stop", code, answer.OutboundStop)
	}
	if e := audited("domain.outbound_restart"); len(e) != 1 || e[0].ActorName != fmt.Sprintf("admin-%d@example.test", operator) ||
		e[0].Target["reason"] != "complaint spike from a compromised mailbox" {
		t.Fatalf("the restart's audit entries: %+v; want one naming who and the stop it lifted", e)
	}
	var after models.OutboundQueue
	if err := tx.First(&after, queued.ID).Error; err != nil {
		t.Fatal(err)
	}
	if gained := after.ExpiresAt.Sub(queued.ExpiresAt); gained < 23*time.Hour || gained > 25*time.Hour || after.Status != "deferred" {
		t.Fatalf("held mail after the restart: status %q, lifetime extended by %s; want deferred and about a day", after.Status, gained)
	}
	var afterFresh models.OutboundQueue
	if err := tx.First(&afterFresh, fresh.ID).Error; err != nil {
		t.Fatal(err)
	}
	if gained := afterFresh.ExpiresAt.Sub(fresh.ExpiresAt); gained > time.Minute {
		t.Fatalf("mail queued during the stop gained %s of lifetime; want only the moments it was held", gained)
	}
	if code, _ := call(operator, http.MethodDelete, ""); code != http.StatusOK || len(audited("domain.outbound_restart")) != 1 {
		t.Fatalf("restarting a running domain: %d; want 200 and nothing more audited", code)
	}
}
