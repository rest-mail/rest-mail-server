package handlers

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/db/models"
)

// A domain's sending policy is set through the admin domain API (#144): it is
// created with one or none, a PATCH changes or clears it, and any other value
// is refused with 422 and changes nothing. Needs the unit-test Postgres; skips
// when none is reachable.
func TestDomainSendingPolicy_SetThroughTheAdminAPI(t *testing.T) {
	gdb := openAuthzTestDB(t)
	if err := gdb.AutoMigrate(&models.Pipeline{}); err != nil {
		t.Skipf("domain sending policy test skipped: migrate failed (%v)", err)
	}
	tx := gdb.Begin()
	t.Cleanup(func() { tx.Rollback() })
	h := NewDomainHandler(tx, nil)

	name := fmt.Sprintf("policy-%d.test", time.Now().UnixNano())
	rr := httptest.NewRecorder()
	domainRoutes(h).ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1/admin/domains",
		bytes.NewBufferString(`{"name":"`+name+`","sending_policy":"bulk"}`)))
	if rr.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201 (body %s)", rr.Code, rr.Body.String())
	}
	var created models.Domain
	if err := tx.Where("name = ?", name).First(&created).Error; err != nil {
		t.Fatalf("find the created domain: %v", err)
	}
	if created.SendingPolicy != models.SendingPolicyBulk {
		t.Fatalf("created sending_policy = %q, want bulk", created.SendingPolicy)
	}

	update := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/domains/"+fmt.Sprint(created.ID), bytes.NewBufferString(body))
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", fmt.Sprint(created.ID))
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
		rr := httptest.NewRecorder()
		domainRoutes(h).ServeHTTP(rr, req)
		return rr
	}
	stored := func() string {
		var d models.Domain
		if err := tx.First(&d, created.ID).Error; err != nil {
			t.Fatalf("reload the domain: %v", err)
		}
		return d.SendingPolicy
	}

	for _, tc := range []struct {
		body, want string
		status     int
	}{
		{`{"sending_policy":"transactional"}`, models.SendingPolicyTransactional, http.StatusOK},
		{`{"sending_policy":"newsletter"}`, models.SendingPolicyTransactional, http.StatusUnprocessableEntity},
		{`{"sending_policy":"Bulk"}`, models.SendingPolicyTransactional, http.StatusUnprocessableEntity},
		{`{"default_quota_bytes":2048}`, models.SendingPolicyTransactional, http.StatusOK},
		{`{"sending_policy":""}`, "", http.StatusOK},
	} {
		rr := update(tc.body)
		if rr.Code != tc.status {
			t.Fatalf("PATCH %s: status = %d, want %d (body %s)", tc.body, rr.Code, tc.status, rr.Body.String())
		}
		if tc.status == http.StatusUnprocessableEntity && errorCodeOf(rr) != "validation_failed" {
			t.Fatalf("PATCH %s: code = %q, want validation_failed", tc.body, errorCodeOf(rr))
		}
		if got := stored(); got != tc.want {
			t.Fatalf("after PATCH %s: sending_policy = %q, want %q", tc.body, got, tc.want)
		}
	}
}

// TestSendMessage_DomainSendingPolicy holds the send API to the sender
// domain's policy (#144): a bulk domain's message without list_unsubscribe is
// refused with 422 list_unsubscribe_required, a transactional domain's with
// one is refused with 422 list_unsubscribe_not_allowed, and neither refusal
// queues anything. The matching sends go out as asked. Needs the unit-test
// Postgres; skips when none is reachable.
func TestSendMessage_DomainSendingPolicy(t *testing.T) {
	h, _, _ := seedSigningDomain(t, "policy-signing")

	for _, tc := range []struct {
		policy, refusedBody, refusedCode, sentBody, sentList string
	}{
		{models.SendingPolicyBulk, "", "list_unsubscribe_required", `{"mailto":"unsub@%s"}`, "<mailto:unsub@%s>"},
		{models.SendingPolicyTransactional, `{"mailto":"unsub@%s"}`, "list_unsubscribe_not_allowed", "", ""},
	} {
		t.Run(tc.policy, func(t *testing.T) {
			domain := fmt.Sprintf("policy-%s-%d.test", tc.policy, time.Now().UnixNano())
			if err := h.db.Create(&models.Domain{Name: domain, SendingPolicy: tc.policy}).Error; err != nil {
				t.Fatalf("seed the domain: %v", err)
			}
			sender := seedSendingMailbox(t, h.db, domain, "news")
			claims := &auth.Claims{AccountID: sender.ID, CredentialKind: "app_password"}
			withDomain := func(s string) string {
				if s == "" {
					return ""
				}
				return fmt.Sprintf(s, domain)
			}

			rr := sendAs(h, claims, sendBody(sender.Address, withDomain(tc.refusedBody)))
			if rr.Code != http.StatusUnprocessableEntity || errorCodeOf(rr) != tc.refusedCode {
				t.Fatalf("status = %d, body %s; want 422 %s", rr.Code, rr.Body.String(), tc.refusedCode)
			}
			var queued int64
			h.db.Model(&models.OutboundQueue{}).Where("sender = ?", sender.Address).Count(&queued)
			if queued != 0 {
				t.Fatalf("a refused send queued %d messages", queued)
			}

			fields := headerFields(sendAndReadQueued(t, h, sender, withDomain(tc.sentBody)))
			got := fields["list-unsubscribe"]
			if tc.sentList == "" && len(got) != 0 {
				t.Fatalf("a transactional send carries List-Unsubscribe %q", got)
			}
			if tc.sentList != "" && (len(got) != 1 || got[0] != withDomain(tc.sentList)) {
				t.Fatalf("List-Unsubscribe = %q, want [%q]", got, withDomain(tc.sentList))
			}
		})
	}
}
