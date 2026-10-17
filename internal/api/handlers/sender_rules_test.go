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
	"github.com/restmail/restmail/internal/api/respond"
	"github.com/restmail/restmail/internal/db/models"
)

// Adding a sender that is already on a domain's allow or block list is a
// conflict, as other duplicate creates are, not a server error (#358). A sender
// may still be on both lists at once.
func TestSenderRules_DuplicateIsConflict(t *testing.T) {
	gdb := openAuthzTestDB(t)
	if err := gdb.AutoMigrate(&models.DomainSenderRule{}); err != nil {
		t.Skipf("sender rule test skipped: migrate failed (%v)", err)
	}
	// The uniqueness lives in this index, which rmdb.AutoMigrate creates.
	if err := gdb.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_domain_sender_rules_unique ON domain_sender_rules(domain_id, pattern, list_type)`).Error; err != nil {
		t.Skipf("sender rule test skipped: unique index (%v)", err)
	}

	type addFunc func(*SenderRuleHandler, http.ResponseWriter, *http.Request)
	cases := []struct {
		list, other   string
		add, addOther addFunc
	}{
		{"allowlist", "blocklist", (*SenderRuleHandler).AddToAllowlist, (*SenderRuleHandler).AddToBlocklist},
		{"blocklist", "allowlist", (*SenderRuleHandler).AddToBlocklist, (*SenderRuleHandler).AddToAllowlist},
	}
	for _, tc := range cases {
		t.Run(tc.list, func(t *testing.T) {
			tx := gdb.Begin()
			t.Cleanup(func() { tx.Rollback() })

			domain := models.Domain{Name: fmt.Sprintf("senders-%d.test", time.Now().UnixNano())}
			if err := tx.Create(&domain).Error; err != nil {
				t.Fatalf("create domain: %v", err)
			}
			h := NewSenderRuleHandler(tx)
			add := func(fn addFunc, list string) *httptest.ResponseRecorder {
				req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/admin/domains/%d/%s", domain.ID, list),
					strings.NewReader(`{"pattern":"spam@evil.test"}`))
				rctx := chi.NewRouteContext()
				rctx.URLParams.Add("id", fmt.Sprint(domain.ID))
				req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
				rr := httptest.NewRecorder()
				fn(h, rr, req)
				return rr
			}

			if rr := add(tc.add, tc.list); rr.Code != http.StatusCreated {
				t.Fatalf("first add status = %d, want 201 (body %s)", rr.Code, rr.Body.String())
			}
			if rr := add(tc.addOther, tc.other); rr.Code != http.StatusCreated {
				t.Fatalf("add to the %s status = %d, want 201 (body %s)", tc.other, rr.Code, rr.Body.String())
			}

			rr := add(tc.add, tc.list)
			if rr.Code != http.StatusConflict {
				t.Fatalf("duplicate add status = %d, want 409 (body %s)", rr.Code, rr.Body.String())
			}
			var resp respond.ErrorResponse
			if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if resp.Error.Code != "already_exists" {
				t.Fatalf("error code = %q, want already_exists", resp.Error.Code)
			}
		})
	}
}

// senderRuleHandlers: every handler under /admin/domains/{id}/allowlist and
// /blocklist, the paths that share the spec's DomainId parameter.
var senderRuleHandlers = map[string]func(*SenderRuleHandler, http.ResponseWriter, *http.Request){
	"ListAllowlist":       (*SenderRuleHandler).ListAllowlist,
	"AddToAllowlist":      (*SenderRuleHandler).AddToAllowlist,
	"RemoveFromAllowlist": (*SenderRuleHandler).RemoveFromAllowlist,
	"ListBlocklist":       (*SenderRuleHandler).ListBlocklist,
	"AddToBlocklist":      (*SenderRuleHandler).AddToBlocklist,
	"RemoveFromBlocklist": (*SenderRuleHandler).RemoveFromBlocklist,
}

func serveSenderRule(h *SenderRuleHandler, fn func(*SenderRuleHandler, http.ResponseWriter, *http.Request), method, id, eid, body string) *httptest.ResponseRecorder {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	rctx.URLParams.Add("eid", eid)
	req := httptest.NewRequest(method, "/", strings.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rr := httptest.NewRecorder()
	fn(h, rr, req)
	return rr
}

// TestSenderRules_RejectDomainIDsBelowOne: the spec's domain ID starts at 1
// (#388), so 0 is refused as a negative or non-numeric ID is, before the
// database is asked (the handler has none here). 0 used to list as 200 [].
func TestSenderRules_RejectDomainIDsBelowOne(t *testing.T) {
	h := NewSenderRuleHandler(nil)
	for _, id := range []string{"0", "-1", "vertrag-not-a-number", "1.5", ""} {
		for name, fn := range senderRuleHandlers {
			rr := serveSenderRule(h, fn, http.MethodPost, id, "1", `{"pattern":"spam@evil.test"}`)
			if rr.Code != http.StatusBadRequest {
				t.Errorf("%s with domain ID %q: status %d, want 400", name, id, rr.Code)
			}
		}
	}
}

// TestSenderRules_DomainIDsFromOneReachTheList: a domain ID inside the spec's
// range is served: added, listed and removed.
func TestSenderRules_DomainIDsFromOneReachTheList(t *testing.T) {
	gdb := openAuthzTestDB(t)
	if err := gdb.AutoMigrate(&models.DomainSenderRule{}); err != nil {
		t.Skipf("sender rule test skipped: migrate failed (%v)", err)
	}
	for _, list := range []string{"Allowlist", "Blocklist"} {
		t.Run(list, func(t *testing.T) {
			tx := gdb.Begin()
			t.Cleanup(func() { tx.Rollback() })

			domain := models.Domain{Name: fmt.Sprintf("senders-%d.test", time.Now().UnixNano())}
			if err := tx.Create(&domain).Error; err != nil {
				t.Fatalf("create domain: %v", err)
			}
			h := NewSenderRuleHandler(tx)
			id := fmt.Sprint(domain.ID)

			rr := serveSenderRule(h, senderRuleHandlers["AddTo"+list], http.MethodPost, id, "", `{"pattern":"someone@partner.test"}`)
			if rr.Code != http.StatusCreated {
				t.Fatalf("add status = %d, want 201 (body %s)", rr.Code, rr.Body.String())
			}
			var added struct {
				Data models.DomainSenderRule `json:"data"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &added); err != nil {
				t.Fatalf("decode add: %v", err)
			}

			rr = serveSenderRule(h, senderRuleHandlers["List"+list], http.MethodGet, id, "", "")
			var listed struct {
				Data []models.DomainSenderRule `json:"data"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &listed); err != nil || rr.Code != http.StatusOK {
				t.Fatalf("list status = %d (%v), want 200 (body %s)", rr.Code, err, rr.Body.String())
			}
			if len(listed.Data) != 1 || listed.Data[0].ID != added.Data.ID {
				t.Fatalf("list = %+v, want only entry %d", listed.Data, added.Data.ID)
			}

			rr = serveSenderRule(h, senderRuleHandlers["RemoveFrom"+list], http.MethodDelete, id, fmt.Sprint(added.Data.ID), "")
			if rr.Code != http.StatusNoContent {
				t.Fatalf("remove status = %d, want 204 (body %s)", rr.Code, rr.Body.String())
			}
		})
	}
}
