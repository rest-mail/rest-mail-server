package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/restmail/restmail/internal/bootstrap"
	"github.com/restmail/restmail/internal/version"
	"golang.org/x/crypto/ssh"
)

func describe(t *testing.T, h *ServerHandler) serverDescription {
	t.Helper()
	rr := httptest.NewRecorder()
	h.Describe(rr, httptest.NewRequest(http.MethodGet, "/api/v1/server", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/server = %d %s", rr.Code, rr.Body)
	}
	var body struct {
		Data serverDescription `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %s: %v", rr.Body, err)
	}
	return body.Data
}

// A fresh server says it is rest-mail, which version, and that it waits to be
// claimed with the code it printed.
func TestServer_AFreshInstallSaysHowToClaimIt(t *testing.T) {
	f := newUnclaimedServer(t)
	none, _ := bootstrap.ParseOwnerKeys("")

	got := describe(t, NewServerHandler(f.db, none))

	want := serverDescription{Product: "rest-mail", Version: version.Version, Claimed: false, ClaimMethods: []string{"setup_code"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

// Listing owner keys adds a way to claim it from anywhere.
func TestServer_OwnerKeysAreAWayToClaim(t *testing.T) {
	f := newUnclaimedServer(t)
	keys, _ := bootstrap.ParseOwnerKeys(string(ssh.MarshalAuthorizedKey(ownerKey(t).PublicKey())))

	got := describe(t, NewServerHandler(f.db, keys))

	if want := []string{"setup_code", "owner_key"}; !reflect.DeepEqual(got.ClaimMethods, want) {
		t.Errorf("claim_methods = %v, want %v", got.ClaimMethods, want)
	}
}

// Once claimed, there is nothing left to claim: the app signs in instead.
func TestServer_OnceClaimedItSaysSo(t *testing.T) {
	f := newUnclaimedServer(t)
	keys, _ := bootstrap.ParseOwnerKeys(string(ssh.MarshalAuthorizedKey(ownerKey(t).PublicKey())))
	claim := post(f.auth.Claim, map[string]string{
		"token": mintSetup(t, f.db), "username": "owner", "password": ownerPassword, "client": "native",
	})
	if claim.Code != http.StatusOK {
		t.Fatalf("claim = %d %s", claim.Code, claim.Body)
	}

	got := describe(t, NewServerHandler(f.db, keys))

	if !got.Claimed || len(got.ClaimMethods) != 0 {
		t.Errorf("got %+v, want claimed with no claim methods", got)
	}
}

// claim_methods is a list even when empty: a client iterating it must not
// meet null.
func TestServer_ClaimMethodsIsNeverNull(t *testing.T) {
	f := newUnclaimedServer(t)
	none, _ := bootstrap.ParseOwnerKeys("")
	claim := post(f.auth.Claim, map[string]string{
		"token": mintSetup(t, f.db), "username": "owner", "password": ownerPassword, "client": "native",
	})
	if claim.Code != http.StatusOK {
		t.Fatalf("claim = %d %s", claim.Code, claim.Body)
	}
	rr := httptest.NewRecorder()
	NewServerHandler(f.db, none).Describe(rr, httptest.NewRequest(http.MethodGet, "/api/v1/server", nil))
	var raw struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &raw)
	if got := string(raw.Data["claim_methods"]); got != "[]" {
		t.Errorf("claim_methods = %s, want []", got)
	}
}
