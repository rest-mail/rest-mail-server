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
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/grants"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func scopedRequest(scopes []string, cap string, handler http.HandlerFunc, path string) *httptest.ResponseRecorder {
	entries := make([]grants.Entry, 0, len(scopes))
	for _, scope := range scopes {
		entries = append(entries, grants.Entry{Scope: scope, Capability: cap})
	}
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req = req.WithContext(context.WithValue(req.Context(), middleware.ClaimsKey, &auth.Claims{AccountID: 1}))
	rr := httptest.NewRecorder()
	middleware.RequireCapabilityOnList(func(uint) (grants.Held, error) { return grants.HeldOf(entries...), nil }, cap)(handler).ServeHTTP(rr, req)
	return rr
}

func TestScopedListSQL(t *testing.T) {
	db, err := gorm.Open(postgres.New(postgres.Config{DSN: "host=127.0.0.1 user=unused dbname=unused sslmode=disable"}), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		resource  string
		scopes    []string
		want      string
		forbidden string
	}{
		{"domains", []string{"domain:a.test"}, "lower(domains.name) IN", "mailbox:"},
		{"domains", []string{"mailbox:x@a.test"}, "1 = 0", "domain_id IN"},
		{"mailboxes", []string{"domain:a.test", "mailbox:x@b.test"}, "lower(mailboxes.address) IN", "LIKE"},
		{"aliases", []string{"mailbox:x@a.test"}, "1 = 0", "lower(mailboxes.address)"},
	} {
		t.Run(tc.resource+strings.Join(tc.scopes, ","), func(t *testing.T) {
			rr := scopedRequest(tc.scopes, "mailboxes:read", func(w http.ResponseWriter, r *http.Request) {
				q := filterAdminList(db.Table(tc.resource), r, "mailboxes:read", tc.resource).Where("domain_id = ?", 99)
				var rows []map[string]interface{}
				result := q.Limit(2).Offset(1).Find(&rows)
				sql := result.Statement.SQL.String()
				if !strings.Contains(sql, tc.want) || strings.Contains(sql, tc.forbidden) {
					t.Errorf("SQL=%s", sql)
				}
				if !strings.Contains(sql, "AND domain_id =") {
					t.Errorf("user filter was not intersected: %s", sql)
				}
				if !strings.Contains(sql, "LIMIT") || !strings.Contains(sql, "OFFSET") {
					t.Errorf("paging lost: %s", sql)
				}
				w.WriteHeader(200)
			}, "/")
			if rr.Code != 200 {
				t.Fatalf("status=%d", rr.Code)
			}
		})
	}
}

func TestScopedListDatabaseIsolation(t *testing.T) {
	db := openAuthzTestDB(t)
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })
	if err := tx.AutoMigrate(&models.Alias{}, &models.QuotaUsage{}); err != nil {
		t.Fatal(err)
	}
	suffix := time.Now().UnixNano()
	a := models.Domain{Name: fmt.Sprintf("scoped-a-%d.test", suffix)}
	b := models.Domain{Name: fmt.Sprintf("scoped-b-%d.test", suffix)}
	for _, d := range []*models.Domain{&a, &b} {
		if err := tx.Create(d).Error; err != nil {
			t.Fatal(err)
		}
	}
	boxes := []models.Mailbox{{DomainID: a.ID, LocalPart: "one", Address: "one@" + a.Name}, {DomainID: a.ID, LocalPart: "two", Address: "two@" + a.Name}, {DomainID: b.ID, LocalPart: "other", Address: "other@" + b.Name}}
	for i := range boxes {
		if err := tx.Create(&boxes[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	aliases := []models.Alias{{DomainID: a.ID, SourceAddress: "alias@" + a.Name, DestinationAddress: "out@remote.test"}, {DomainID: b.ID, SourceAddress: "alias@" + b.Name, DestinationAddress: "out@remote.test"}}
	for i := range aliases {
		if err := tx.Create(&aliases[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	domainList := func(scopes []string, path string) listEnvelope {
		rr := scopedRequest(scopes, middleware.CapDomainsRead, domainRoutes(NewDomainHandler(tx, nil)).ServeHTTP, path)
		if rr.Code != 200 {
			t.Fatalf("domains status%d body%s", rr.Code, rr.Body.String())
		}
		var out listEnvelope
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	got := domainList([]string{grants.Domain(a.Name)}, "/api/v1/admin/domains?limit=1")
	if len(got.Data) != 1 || got.Pagination.Total != 1 || got.Pagination.HasMore {
		t.Fatalf("domain scope/total leaked: %+v", got)
	}
	empty := domainList([]string{grants.Mailbox(boxes[0].Address)}, "/api/v1/admin/domains")
	if len(empty.Data) != 0 || empty.Pagination.Total != 0 {
		t.Fatalf("mailbox learned parent: %+v", empty)
	}
	rr := scopedRequest([]string{grants.Domain(a.Name)}, middleware.CapMailboxesRead, mailboxRoutes(NewMailboxHandler(tx, nil, nil)).ServeHTTP, "/api/v1/admin/mailboxes?limit=1&offset=1")
	var page listEnvelope
	if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if rr.Code != 200 || len(page.Data) != 1 || page.Pagination.Total != 2 || page.Pagination.HasMore {
		t.Fatalf("mailbox paging status%d body%s", rr.Code, rr.Body.String())
	}
	rr = scopedRequest([]string{grants.Mailbox(boxes[0].Address)}, middleware.CapMailboxesRead, mailboxRoutes(NewMailboxHandler(tx, nil, nil)).ServeHTTP, "/api/v1/admin/mailboxes")
	var mailboxPage struct {
		Data       []models.Mailbox
		Pagination struct{ Total int }
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &mailboxPage); err != nil {
		t.Fatal(err)
	}
	if rr.Code != 200 || len(mailboxPage.Data) != 1 || mailboxPage.Data[0].ID != boxes[0].ID || mailboxPage.Pagination.Total != 1 {
		t.Fatalf("mailbox scope leaked: %s", rr.Body.String())
	}
	if mailboxPage.Data[0].Domain.Name != "" {
		t.Fatalf("parent configuration leaked: %s", rr.Body.String())
	}
	rr = scopedRequest([]string{grants.Mailbox(boxes[0].Address)}, middleware.CapMailboxesRead, aliasRoutes(NewAliasHandler(tx)).ServeHTTP, "/api/v1/admin/aliases")
	// A fresh envelope each time: total is omitted at 0, and decoding into the
	// last page would keep its total.
	page = listEnvelope{}
	if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if rr.Code != 200 || len(page.Data) != 0 || page.Pagination.Total != 0 {
		t.Fatalf("mailbox learned aliases: %s", rr.Body.String())
	}
	rr = scopedRequest([]string{grants.Domain(a.Name)}, middleware.CapMailboxesRead, aliasRoutes(NewAliasHandler(tx)).ServeHTTP, "/api/v1/admin/aliases?domain_id="+fmt.Sprint(b.ID))
	page = listEnvelope{}
	if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if rr.Code != 200 || len(page.Data) != 0 || page.Pagination.Total != 0 {
		t.Fatalf("query widened alias scope: %s", rr.Body.String())
	}
	for _, id := range []uint{aliases[0].ID, aliases[1].ID} {
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", fmt.Sprint(id))
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
		scope, found, err := AliasScopeOf(tx)(req)
		want := grants.Domain(a.Name)
		if id == aliases[1].ID {
			want = grants.Domain(b.Name)
		}
		if err != nil || !found || scope != want {
			t.Fatalf("alias scope=%q,%v,%v want%q", scope, found, err, want)
		}
	}
}

func TestScopedParentDomainRedaction(t *testing.T) {
	for _, allow := range []bool{false, true} {
		entries := []grants.Entry{{Scope: "mailbox:x@a.test", Capability: "mailboxes:read"}}
		if allow {
			entries = append(entries, grants.Entry{Scope: "domain:a.test", Capability: "domains:read"})
		}
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req = req.WithContext(context.WithValue(req.Context(), middleware.ClaimsKey, &auth.Claims{AccountID: 1}))
		rr := httptest.NewRecorder()
		middleware.RequireCapability(func(uint) (grants.Held, error) { return grants.HeldOf(entries...), nil }, "mailboxes:read", func(*http.Request) (string, bool, error) { return "mailbox:x@a.test", true, nil })(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			box := models.Mailbox{Domain: models.Domain{Name: "a.test", SendingPolicy: "bulk", DKIMSelector: "private-setup"}}
			redactMailboxDomain(r, &box)
			if (box.Domain.Name != "") != allow {
				t.Errorf("redaction allow%v domain%+v", allow, box.Domain)
			}
			w.WriteHeader(200)
		})).ServeHTTP(rr, req)
		if rr.Code != 200 {
			t.Fatal(rr.Code)
		}
	}
}

func TestScopedDomainBodyCheck(t *testing.T) {
	rr := scopedRequest([]string{"domain:a.test"}, "mailboxes:write", func(w http.ResponseWriter, r *http.Request) {
		if !scopedDomainAllowed(r, "mailboxes:write", "a.test") || scopedDomainAllowed(r, "mailboxes:write", "b.test") {
			t.Error("body domain check widened scope")
		}
		w.WriteHeader(200)
	}, "/")
	if rr.Code != 200 {
		t.Fatal(rr.Code)
	}
}

func TestScopedParentDomainKeyCeiling(t *testing.T) {
	claims := &auth.Claims{AccountID: 1, CredentialID: 3, CredentialKind: auth.CredentialKindAPIKey, APIKeyPermissions: []auth.ScopedPermission{{Capability: "mailboxes:read", Scope: "domain:a.test"}}}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(context.WithValue(req.Context(), middleware.ClaimsKey, claims))
	rr := httptest.NewRecorder()
	middleware.RequireCapabilityOnList(func(uint) (grants.Held, error) { return grants.HeldOf(grants.Entry{Capability: "*", Scope: "*"}), nil }, "mailboxes:read")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		box := models.Mailbox{Domain: models.Domain{Name: "a.test", SendingPolicy: "bulk", DKIMSelector: "sensitive"}}
		redactMailboxDomain(r, &box)
		if box.Domain.Name != "" || box.Domain.SendingPolicy != "" || box.Domain.DKIMSelector != "" {
			t.Fatal("owner's broad grant escaped key ceiling")
		}
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status%d body%s", rr.Code, rr.Body.String())
	}
}
