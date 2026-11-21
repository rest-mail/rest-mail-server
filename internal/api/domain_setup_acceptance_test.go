package api

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/grants"
)

func TestAC01DomainNamingAcrossRoutes(t *testing.T) {
	f := newGrantsFixture(t)
	routers := NewRouters(f.db, auth.NewJWTService("domain-naming-test-key", 15*time.Minute, 24*time.Hour), &config.Config{
		Environment: "test", GatewayHostname: "mail.example.test.", TOTP2FAEnabled: true, MasterKey: accessMasterKey,
	}, nil)
	t.Cleanup(routers.Close)
	f.router = routers.Public
	owner := f.token(t, "owner")
	const domains = "/api/v1/admin/domains"
	const aliases = "/api/v1/admin/aliases"
	const discovery = "/.well-known/restmail?domain="
	const mozilla = "/.well-known/autoconfig/mail/config-v1.1.xml?emailaddress="
	const mozillaMX = "/mail/config-v1.1.xml?emailaddress="
	const outlook = "/autodiscover/autodiscover.xml?emailaddress="
	aliasBody := func(domain string) string {
		return fmt.Sprintf(`{"source_address":%q,"destination_address":"owner@a.test"}`, "sales@"+domain)
	}
	for _, name := range []string{"example..com", ".example.com", "", "..", "example.com..", "example.com。。"} {
		t.Run(fmt.Sprintf("invalid %q", name), func(t *testing.T) {
			mail := url.QueryEscape("sales@" + name)
			for _, probe := range []struct{ method, path, body string }{
				{http.MethodPost, domains, fmt.Sprintf(`{"name":%q}`, name)},
				{http.MethodPost, aliases, aliasBody(name)},
				{http.MethodGet, discovery + url.QueryEscape(name), ""},
				{http.MethodGet, mozilla + mail, ""},
				{http.MethodGet, mozillaMX + mail, ""},
				{http.MethodPost, outlook + mail, ""},
			} {
				rr := doRequest(f.router, probe.method, probe.path, owner, probe.body)
				if rr.Code != http.StatusUnprocessableEntity || grantErrorCode(rr) != "validation_failed" {
					t.Errorf("%s %s: got %d %s, want 422 validation_failed", probe.method, probe.path, rr.Code, rr.Body)
				}
			}
		})
	}
	created := doRequest(f.router, http.MethodPost, domains, owner, `{"name":"EXAMPLE.COM."}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("absolute domain creation: got %d %s, want 201", created.Code, created.Body)
	}
	var domain models.Domain
	if err := f.db.Where("name = ?", "example.com").First(&domain).Error; err != nil {
		t.Fatalf("canonical domain not stored: %v", err)
	}
	alias := doRequest(f.router, http.MethodPost, aliases, owner, aliasBody("EXAMPLE.COM."))
	if alias.Code != http.StatusCreated {
		t.Errorf("absolute alias source: got %d %s, want 201", alias.Code, alias.Body)
	} else {
		var stored models.Alias
		if err := f.db.Where("source_address = ? AND domain_id = ?", "sales@example.com", domain.ID).First(&stored).Error; err != nil {
			t.Errorf("canonical alias not stored on its domain: %v", err)
		}
	}
	for _, probe := range []struct{ method, path string }{
		{http.MethodGet, discovery + "A.TEST."},
		{http.MethodGet, mozilla + "sales%40A.TEST."},
		{http.MethodGet, mozillaMX + "sales%40A.TEST."},
		{http.MethodPost, outlook + "sales%40A.TEST."},
	} {
		rr := doRequest(f.router, probe.method, probe.path, "", "")
		if rr.Code != http.StatusOK {
			t.Errorf("absolute lookup %s: got %d %s, want 200", probe.path, rr.Code, rr.Body)
		}
	}
}

func TestAC02DelegatedDomainAdministratorSetup(t *testing.T) {
	f := newGrantsFixture(t, fixtureAdmin{name: "delegate", twoFactor: true})
	owner := f.token(t, "owner")
	created := doRequest(f.router, http.MethodPost, "/api/v1/admin/domains", owner, `{"name":"delegated.test"}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("global administrator provisioning: got %d %s, want 201", created.Code, created.Body)
	}
	var own models.Domain
	if err := f.db.Where("name = ?", "delegated.test").First(&own).Error; err != nil {
		t.Fatal(err)
	}
	var postmaster models.Mailbox
	if err := f.db.Where("address = ? AND domain_id = ? AND active", "postmaster@delegated.test", own.ID).First(&postmaster).Error; err != nil {
		t.Fatalf("active postmaster not provisioned: %v", err)
	}
	postmasterGrants, err := grants.Load(f.db, postmaster.ID, time.Now())
	if err != nil || !postmasterGrants.Allows(models.CapMailReceive, grants.Mailbox(postmaster.Address)) {
		t.Fatalf("postmaster cannot receive reports: %v", err)
	}
	if rr := f.grant(t, owner, f.admins["delegate"].user.ID, "admin", "domain:delegated.test"); rr.Code != http.StatusCreated {
		t.Fatalf("grant: got %d %s, want 201", rr.Code, rr.Body)
	}
	delegate := f.token(t, "delegate")
	ownPath := fmt.Sprintf("/api/v1/admin/domains/%d", own.ID)
	foreignPath := fmt.Sprintf("/api/v1/admin/domains/%d", f.b.ID)
	const policy = `{"mode":"testing","mx_hosts":"mail.delegated.test","max_age":86400}`
	for _, step := range []struct {
		name, method, path, body string
		status                   int
	}{
		{"own domain", http.MethodGet, ownPath, "", 200},
		{"own DNS screen", http.MethodGet, ownPath + "/dns", "", 200},
		{"own domain settings", http.MethodPatch, ownPath, `{"dmarc_policy":"quarantine"}`, 200},
		{"own mailbox", http.MethodPost, "/api/v1/accounts", `{"address":"reports@delegated.test"}`, 201},
		{"own policy", http.MethodPut, ownPath + "/mta-sts", policy, 201},
		{"own policy read", http.MethodGet, ownPath + "/mta-sts", "", 200},
		{"own report list", http.MethodGet, "/api/v1/admin/dmarc-reports", "", 200},
	} {
		t.Run(step.name, func(t *testing.T) {
			rr := doRequest(f.router, step.method, step.path, delegate, step.body)
			if rr.Code != step.status {
				t.Errorf("%s %s: got %d %s, want %d", step.method, step.path, rr.Code, rr.Body, step.status)
			}
		})
	}
	for _, step := range []struct{ name, method, path, body string }{
		{"another domain", http.MethodGet, foreignPath, ""},
		{"another DNS screen", http.MethodGet, foreignPath + "/dns", ""},
		{"another domain settings", http.MethodPatch, foreignPath, `{"dmarc_policy":"quarantine"}`},
		{"another mailbox", http.MethodPost, "/api/v1/accounts", `{"address":"reports@b.test"}`},
		{"another policy", http.MethodPut, foreignPath + "/mta-sts", policy},
		{"global domain creation", http.MethodPost, "/api/v1/admin/domains", `{"name":"not-delegated.test"}`},
		{"global queue", http.MethodGet, "/api/v1/admin/queue", ""},
		{"global DKIM list", http.MethodGet, "/api/v1/admin/dkim", ""},
		{"global certificate list", http.MethodGet, "/api/v1/admin/certificates", ""},
		{"global relay write", http.MethodPut, ownPath + "/relay", `{"transport":"direct"}`},
	} {
		t.Run(step.name, func(t *testing.T) {
			rr := doRequest(f.router, step.method, step.path, delegate, step.body)
			if rr.Code != http.StatusForbidden || grantErrorCode(rr) != "forbidden" {
				t.Errorf("%s %s: got %d %s, want 403 forbidden", step.method, step.path, rr.Code, rr.Body)
			}
		})
	}
	const missing = "/api/v1/admin/domains/999999/dns"
	if rr := doRequest(f.router, http.MethodGet, missing, delegate, ""); rr.Code != http.StatusForbidden || grantErrorCode(rr) != "forbidden" {
		t.Errorf("missing domain as delegate: got %d %s, want 403 forbidden", rr.Code, rr.Body)
	}
	if rr := doRequest(f.router, http.MethodGet, missing, owner, ""); rr.Code != http.StatusNotFound || grantErrorCode(rr) != "domain_not_found" {
		t.Errorf("missing domain as global administrator: got %d %s, want 404 domain_not_found", rr.Code, rr.Body)
	}
	if err := f.db.First(&own, own.ID).Error; err != nil || own.DMARCPolicy != models.DMARCPolicyQuarantine {
		t.Errorf("own DMARC policy not persisted: %q (%v)", own.DMARCPolicy, err)
	}
	var stored models.MTASTSPolicy
	if err := f.db.Where("domain_id = ?", own.ID).First(&stored).Error; err != nil || stored.Mode != "testing" || stored.MaxAge != 86400 {
		t.Errorf("own policy not persisted: %+v (%v)", stored, err)
	}
	var mailbox models.Mailbox
	if err := f.db.Where("address = ? AND domain_id = ?", "reports@delegated.test", own.ID).First(&mailbox).Error; err != nil {
		t.Errorf("own mailbox not persisted: %v", err)
	}
	var refusedWrites int64
	if err := f.db.Model(&models.Domain{}).Where("name = ?", "not-delegated.test").Count(&refusedWrites).Error; err != nil || refusedWrites != 0 {
		t.Errorf("global domain refusal left %d rows: %v", refusedWrites, err)
	}
}
