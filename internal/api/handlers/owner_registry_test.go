package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/bootstrap"
	"golang.org/x/crypto/ssh"
)

func TestOwnerRegistry_HTTPUsesCurrentDatabaseKeys(t *testing.T) {
	f := newInstalledServer(t)
	owner, added := ownerKey(t), ownerKey(t)
	h := ownerHandler(t, f, owner)
	// Reconstruct the handler without any deployment owner-key input.
	restarted := NewOwnerHandler(f.db, nil, "owner-handler-secret")
	if rr := askAsOwner(t, restarted, owner, "reset-admin", f.name); rr.Code != http.StatusOK {
		t.Fatalf("recovery after restart without owner config = %d %s", rr.Code, rr.Body)
	}
	if _, err := bootstrap.RegisterOwnerKeys(f.db, string(ssh.MarshalAuthorizedKey(added.PublicKey()))); err != nil {
		t.Fatal(err)
	}
	if rr := askAsOwner(t, h, added, "reset-admin", f.name); rr.Code != http.StatusOK {
		t.Fatalf("running handler did not see newly registered key = %d %s", rr.Code, rr.Body)
	}
	if err := bootstrap.RevokeOwnerKey(f.db, ssh.FingerprintSHA256(owner.PublicKey()), time.Now()); err != nil {
		t.Fatal(err)
	}
	if rr := askAsOwner(t, h, owner, "reset-admin", f.name); rr.Code != http.StatusForbidden || errorCodeOf(rr) != "not_owner" {
		t.Fatalf("running handler accepted revoked key = %d %s", rr.Code, rr.Body)
	}
}

func TestOwnerRegistry_DatabaseFailureFailsClosed(t *testing.T) {
	f := newInstalledServer(t)
	h := ownerHandler(t, f, ownerKey(t))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rr := httptest.NewRecorder()
	ownerRoute(h, "challenge")(rr, httptest.NewRequest(http.MethodPost, "/", nil).WithContext(ctx))
	if rr.Code != http.StatusInternalServerError || errorCodeOf(rr) != "internal_error" {
		t.Fatalf("unavailable database = %d %s, want a closed failure", rr.Code, rr.Body)
	}
}
