package handlers

import (
	"reflect"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/testdb"
)

func TestEnsureRBACRenamesCredentialScopesAfterSeeding(t *testing.T) {
	shared, err := connectTestDB()
	if err != nil {
		testdb.Unreachable(t, "no database reachable (%v)", err)
	}
	database := shared.Begin()
	if database.Error != nil {
		t.Fatal(database.Error)
	}
	t.Cleanup(func() { database.Rollback() })
	if err := ensureRBAC(database); err != nil {
		t.Fatal(err)
	}
	credential := models.Credential{
		PublicID: "rbac-scope-cache",
		Kind:     models.CredentialKindAppPassword,
		Scope:    models.CredentialScope{Capabilities: []string{"mailboxes:read", "mailboxes:write", "mailboxes:delete"}},
	}
	if err := database.Create(&credential).Error; err != nil {
		t.Fatal(err)
	}
	if err := ensureRBAC(database); err != nil {
		t.Fatal(err)
	}
	var stored models.Credential
	if err := database.First(&stored, credential.ID).Error; err != nil {
		t.Fatal(err)
	}
	want := []string{"accounts:read", "accounts:write", "accounts:delete"}
	if !reflect.DeepEqual(stored.Scope.Capabilities, want) {
		t.Fatalf("cached RBAC left legacy credential capabilities: got %v, want %v", stored.Scope.Capabilities, want)
	}
}
