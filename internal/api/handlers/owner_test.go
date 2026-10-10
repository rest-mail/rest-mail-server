package handlers

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/restmail/restmail/internal/bootstrap"
	"golang.org/x/crypto/ssh"
)

// The owner-key endpoints, end to end over HTTP: challenge, sign, token, and
// the token claims the server.

func ownerKey(t *testing.T) ssh.Signer {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func ownerHandler(t *testing.T, f *firstSignIn, listed ...ssh.Signer) *OwnerHandler {
	t.Helper()
	var spec []string
	for _, s := range listed {
		spec = append(spec, string(ssh.MarshalAuthorizedKey(s.PublicKey())))
	}
	keys, err := bootstrap.ParseOwnerKeys(strings.Join(spec, ""))
	if err != nil {
		t.Fatal(err)
	}
	return NewOwnerHandler(f.db, keys, "owner-handler-secret")
}

// askAsOwner does what the CLI does and returns the token response.
func askAsOwner(t *testing.T, h *OwnerHandler, s ssh.Signer, purpose, username string) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	h.Challenge(rr, httptest.NewRequest(http.MethodPost, "/", nil))
	var ch struct {
		Data struct {
			Challenge string `json:"challenge"`
		} `json:"data"`
	}
	if rr.Code != http.StatusOK || json.Unmarshal(rr.Body.Bytes(), &ch) != nil {
		return rr
	}
	sig, err := s.Sign(rand.Reader, bootstrap.OwnerMessage(ch.Data.Challenge, purpose, username))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{
		"challenge":  ch.Data.Challenge,
		"public_key": string(ssh.MarshalAuthorizedKey(s.PublicKey())),
		"signature":  base64.StdEncoding.EncodeToString(ssh.Marshal(sig)),
		"purpose":    purpose,
		"username":   username,
	})
	rr = httptest.NewRecorder()
	h.Token(rr, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(b)))
	return rr
}

func TestOwner_SignedChallengeClaimsTheServer(t *testing.T) {
	f := newUnclaimedServer(t)
	owner := ownerKey(t)
	h := ownerHandler(t, f, owner)

	rr := askAsOwner(t, h, owner, "setup", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("token = %d %s", rr.Code, rr.Body)
	}
	var got struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &got)
	claim := post(f.auth.Claim, map[string]string{
		"token": got.Data.Token, "username": "owner", "password": ownerPassword, "client": "native",
	})
	if claim.Code != http.StatusOK {
		t.Errorf("claim with the owner's token = %d %s", claim.Code, claim.Body)
	}
	// Once claimed, setup is refused: the owner resets an admin instead.
	if rr := askAsOwner(t, h, owner, "setup", ""); rr.Code != http.StatusConflict || claimErrorCode(rr) != "already_claimed" {
		t.Errorf("second setup = %d %s, want 409 already_claimed", rr.Code, rr.Body)
	}
	if rr := askAsOwner(t, h, owner, "reset-admin", "nobody"); rr.Code != http.StatusNotFound || claimErrorCode(rr) != "no_such_admin" {
		t.Errorf("reset for nobody = %d %s, want 404 no_such_admin", rr.Code, rr.Body)
	}
	if rr := askAsOwner(t, h, owner, "reset-admin", "owner"); rr.Code != http.StatusOK {
		t.Errorf("reset for owner = %d %s", rr.Code, rr.Body)
	}
}

func TestOwner_Refusals(t *testing.T) {
	f := newUnclaimedServer(t)
	owner, stranger := ownerKey(t), ownerKey(t)

	if rr := askAsOwner(t, ownerHandler(t, f, owner), stranger, "setup", ""); rr.Code != http.StatusForbidden || claimErrorCode(rr) != "not_owner" {
		t.Errorf("a stranger's key = %d %s, want 403 not_owner", rr.Code, rr.Body)
	}
	if rr := askAsOwner(t, ownerHandler(t, f), owner, "setup", ""); rr.Code != http.StatusConflict || claimErrorCode(rr) != "no_owner_keys" {
		t.Errorf("no owner keys listed = %d %s, want 409 no_owner_keys", rr.Code, rr.Body)
	}
	if rr := askAsOwner(t, ownerHandler(t, f, owner), owner, "become-root", ""); rr.Code != http.StatusUnprocessableEntity && rr.Code != http.StatusBadRequest {
		t.Errorf("an unknown purpose = %d %s, want a validation error", rr.Code, rr.Body)
	}
	if rr := askAsOwner(t, ownerHandler(t, f, owner), owner, "reset-admin", ""); rr.Code != http.StatusUnprocessableEntity && rr.Code != http.StatusBadRequest {
		t.Errorf("reset without a username = %d %s, want a validation error", rr.Code, rr.Body)
	}
}
