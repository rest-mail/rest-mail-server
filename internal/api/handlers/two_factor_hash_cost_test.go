package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"golang.org/x/crypto/bcrypt"
)

// TestShardWork_EnrollingHashesEveryRecoveryCodeAtTheMinimumCost is the path a
// session test's setup spent its time in (#1942): every recovery code still
// verifies, and only for itself.
func TestShardWork_EnrollingHashesEveryRecoveryCodeAtTheMinimumCost(t *testing.T) {
	store := newFakeStore()
	handler := newTFHandler(store)
	request := withClaims(httptest.NewRequest(http.MethodPost, "/", nil), mailboxClaims(1, "user@example.test"))
	response, err := handler.enroll(request, struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	codes := store.live(1, models.CredentialKindRecoveryCode)
	if len(codes) != len(response.RecoveryCodes) || len(codes) == 0 {
		t.Fatalf("stored %d codes, returned %d", len(codes), len(response.RecoveryCodes))
	}
	for index, credential := range codes {
		cost, err := bcrypt.Cost([]byte(strings.TrimPrefix(credential.SecretHash, "{BLF-CRYPT}")))
		if err != nil || cost != bcrypt.MinCost {
			t.Errorf("code %d hash cost = %d, %v; want %d", index, cost, err, bcrypt.MinCost)
		}
		if !auth.CheckRecoveryCode(response.RecoveryCodes[index], credential.SecretHash) {
			t.Errorf("code %d does not verify against its hash", index)
		}
		if auth.CheckRecoveryCode("wrong-code", credential.SecretHash) {
			t.Errorf("code %d verified a different code", index)
		}
	}
}

func TestShardWork_FixturesHashAtTheMinimumCost(t *testing.T) {
	stored, err := auth.HashPassword("password")
	if err != nil {
		t.Fatal(err)
	}
	cost, err := bcrypt.Cost([]byte(strings.TrimPrefix(stored, "{BLF-CRYPT}")))
	if err != nil || cost != bcrypt.MinCost {
		t.Errorf("password hash cost = %d, %v; want %d", cost, err, bcrypt.MinCost)
	}
}
