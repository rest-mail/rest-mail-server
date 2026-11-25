package api

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/db/repositories"
	"github.com/rest-mail/rest-mail-server/internal/examplepairs"
	"github.com/rest-mail/rest-mail-server/internal/grants"
)

var postmasterExamplePaths = map[string]string{
	"updateMailbox": "/api/v1/admin/mailboxes/%d",
	"deleteMailbox": "/api/v1/admin/mailboxes/%d",
	"updateAlias":   "/api/v1/admin/aliases/%d",
	"deleteAlias":   "/api/v1/admin/aliases/%d",
	"deleteGrant":   "/api/v1/admin/grants/%d",
}

func postmasterExampleFixture(t *testing.T, operation string) (*credentialFixture, string) {
	t.Helper()
	previousHeld, previousSessions := heldGrants, sessionCheck
	heldGrants, sessionCheck = cachedHeldGrants, cachedSessionCheck
	defer func() { heldGrants, sessionCheck = previousHeld, previousSessions }()
	fixture := newCredentialFixture(t)
	var superadmin models.Role
	if err := fixture.db.Where("name = ?", "superadmin").First(&superadmin).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := grants.EnsureSystem(fixture.db, fixture.accountID, superadmin.ID, grants.All, models.GrantActive); err != nil {
		t.Fatal(err)
	}
	routers := NewRouters(fixture.db, fixture.jwt, &config.Config{Environment: "test"}, nil)
	t.Cleanup(routers.Close)
	fixture.router = routers.Public
	created := fixture.do(http.MethodPost, "/api/v1/admin/domains", fixture.session, `{"name":"postmaster.test"}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("provision domain: %d %s", created.Code, created.Body)
	}
	var mailbox models.Mailbox
	if err := fixture.db.Where("address = ?", "postmaster@postmaster.test").First(&mailbox).Error; err != nil {
		t.Fatal(err)
	}
	identifier := mailbox.ID
	if operation == "updateAlias" || operation == "deleteAlias" {
		alias := models.Alias{DomainID: mailbox.DomainID, SourceAddress: mailbox.Address, DestinationAddress: fixture.address, Active: true}
		if err := fixture.db.Create(&alias).Error; err != nil {
			t.Fatal(err)
		}
		removed := fixture.do(http.MethodDelete, fmt.Sprintf("/api/v1/admin/mailboxes/%d", mailbox.ID), fixture.session, "")
		if removed.Code != http.StatusNoContent {
			t.Fatalf("replace account with alias: %d %s", removed.Code, removed.Body)
		}
		identifier = alias.ID
	}
	if operation == "deleteGrant" {
		var receiver models.Role
		if err := fixture.db.Where("name = ?", models.RoleReceiver).First(&receiver).Error; err != nil {
			t.Fatal(err)
		}
		var grant models.Grant
		if err := fixture.db.Where("holder_kind = ? AND holder_id = ? AND role_id = ?", models.GrantHolderAccount, mailbox.ID, receiver.ID).First(&grant).Error; err != nil {
			t.Fatal(err)
		}
		identifier = grant.ID
	}
	return fixture, fmt.Sprintf(postmasterExamplePaths[operation], identifier)
}

func TestPostmasterRequiredExamples(t *testing.T) {
	testPostmasterRequiredExamples(t, "")
}

func TestPostmasterGrantRefusalPreservesRouteAndAudit(t *testing.T) {
	fixture, path := postmasterExampleFixture(t, "deleteGrant")
	grantID := path[strings.LastIndex(path, "/")+1:]
	const address = "postmaster@postmaster.test"
	var domain models.Domain
	if err := fixture.db.Where("name = ?", "postmaster.test").First(&domain).Error; err != nil {
		t.Fatal(err)
	}
	before, err := repositories.ResolveSetupRecipient(fixture.db, address, domain.ID)
	if err != nil || before.Mailbox.ID == 0 {
		t.Fatalf("resolve postmaster before removal: mailbox %d, error %v", before.Mailbox.ID, err)
	}
	response := fixture.do(http.MethodDelete, path, fixture.session, "")
	if response.Code != http.StatusConflict || grantErrorCode(response) != "postmaster_required" {
		t.Fatalf("remove its receive grant: %d %s; want 409 postmaster_required", response.Code, response.Body)
	}
	var retained models.Grant
	if err := fixture.db.Where("id = ?", grantID).First(&retained).Error; err != nil || retained.HolderID != before.Mailbox.ID || retained.Mode != models.GrantActive {
		t.Fatalf("retain active receiver grant %s for mailbox %d: holder %d, mode %s, error %v", grantID, before.Mailbox.ID, retained.HolderID, retained.Mode, err)
	}
	after, err := repositories.ResolveSetupRecipient(fixture.db, address, domain.ID)
	if err != nil || after.Mailbox.ID != before.Mailbox.ID {
		t.Fatalf("resolve postmaster after refusal: mailbox %d, error %v; want mailbox %d", after.Mailbox.ID, err, before.Mailbox.ID)
	}
	var entries []models.AuditEntry
	if err := fixture.db.Where("method = ?", http.MethodDelete).Find(&entries).Error; err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("audit DELETE events: got %d; want only the refusal", len(entries))
	}
	entry := entries[0]
	if entry.Outcome != models.AuditOutcomeFailed || entry.Status != http.StatusConflict || entry.ErrorCode != "postmaster_required" || entry.Route != "/api/v1/admin/grants/{id}" || entry.Target["id"] != grantID {
		t.Fatalf("audit refusal: %s %d %s %s target %v; want failed 409 postmaster_required for grant %s", entry.Outcome, entry.Status, entry.ErrorCode, entry.Route, entry.Target, grantID)
	}
}

func TestPostmasterRequiredExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY for postmaster refusal exchanges")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	testPostmasterRequiredExamples(t, binary)
}

// testPostmasterRequiredExamples sends each refusal to the real router, and through Vertrag too
// when vertrag names its binary. Only the test named in contract:pairs passes one.
func testPostmasterRequiredExamples(t *testing.T, vertrag string) {
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	for operation := range postmasterExamplePaths {
		t.Run(operation, func(t *testing.T) {
			fixture, path := postmasterExampleFixture(t, operation)
			pair := pairs[operation]["postmaster_required"]
			method := http.MethodPatch
			if operation[:6] == "delete" {
				method = http.MethodDelete
			}
			response := fixture.do(method, path, fixture.session, string(pair.Request.Body))
			if response.Code != pair.Response.Status {
				t.Fatalf("refusal: %d %s; want %d", response.Code, response.Body, pair.Response.Status)
			}
			assertPublicExampleBody(t, pair.Response.Body, response.Body.Bytes())
			examplepairs.Proven(t, operation, "postmaster_required")
			if vertrag != "" {
				scenario := bulkQueueExampleScenario{accountReadScenario{fixture.router, fixture.session[len("Bearer "):]}, nil}
				identifier, err := strconv.Atoi(path[strings.LastIndex(path, "/")+1:])
				if err != nil {
					t.Fatal(err)
				}
				pair.Request.Path = map[string]interface{}{"id": identifier}
				route := strings.Replace(postmasterExamplePaths[operation], "%d", "{id}", 1)
				checkQueueMutationPairWithVertrag(t, vertrag, scenario, method, route, "postmaster_required", pair, 513)
			}
		})
	}
}
