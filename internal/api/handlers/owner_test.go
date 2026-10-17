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

// The owner-key endpoints, end to end over HTTP: challenge, sign, and a token
// that lets an admin back in.

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
	return keysHandler(t, f, listed, nil)
}

// keysHandler lists owners in RESTMAIL_OWNER_KEYS and status in
// RESTMAIL_STATUS_KEYS.
func keysHandler(t *testing.T, f *firstSignIn, owners, status []ssh.Signer) *OwnerHandler {
	t.Helper()
	spec := func(signers []ssh.Signer) string {
		var lines []string
		for _, s := range signers {
			lines = append(lines, string(ssh.MarshalAuthorizedKey(s.PublicKey())))
		}
		return strings.Join(lines, "")
	}
	keys, err := bootstrap.ParseOwnerKeys(spec(owners))
	if err != nil {
		t.Fatal(err)
	}
	statusKeys, err := bootstrap.ParseStatusKeys(spec(status))
	if err != nil {
		t.Fatal(err)
	}
	return NewOwnerHandler(f.db, keys, statusKeys, "owner-handler-secret")
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

func TestOwner_SignedChallengeLetsAnAdminBackIn(t *testing.T) {
	f := newInstalledServer(t)
	f.signInFirstTime(t, chosenPassword)
	owner := ownerKey(t)
	h := ownerHandler(t, f, owner)

	if rr := askAsOwner(t, h, owner, "reset-admin", "nobody"); rr.Code != http.StatusNotFound || errorCodeOf(rr) != "no_such_admin" {
		t.Errorf("reset for nobody = %d %s, want 404 no_such_admin", rr.Code, rr.Body)
	}
	rr := askAsOwner(t, h, owner, "reset-admin", f.name)
	if rr.Code != http.StatusOK {
		t.Fatalf("token = %d %s", rr.Code, rr.Body)
	}
	var got struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &got)
	if reset := post(f.auth.Reset, map[string]string{"token": got.Data.Token, "password": ownerPassword, "client": "native"}); reset.Code != http.StatusOK {
		t.Errorf("reset with the owner's token = %d %s", reset.Code, reset.Body)
	}
}

// Before 0.7 an owner key could also claim a server. Nothing claims one now:
// its first admin is the bootstrap one.
func TestOwner_NothingToClaim(t *testing.T) {
	f := newInstalledServer(t)
	owner := ownerKey(t)
	rr := askAsOwner(t, ownerHandler(t, f, owner), owner, "setup", "")
	if rr.Code != http.StatusUnprocessableEntity || errorCodeOf(rr) != "validation_failed" {
		t.Errorf("setup = %d %s, want 422 validation_failed", rr.Code, rr.Body)
	}
}

func TestOwner_Refusals(t *testing.T) {
	f := newInstalledServer(t)
	owner, stranger := ownerKey(t), ownerKey(t)

	if rr := askAsOwner(t, ownerHandler(t, f, owner), stranger, "reset-admin", f.name); rr.Code != http.StatusForbidden || errorCodeOf(rr) != "not_owner" {
		t.Errorf("a stranger's key = %d %s, want 403 not_owner", rr.Code, rr.Body)
	}
	if rr := askAsOwner(t, ownerHandler(t, f), owner, "reset-admin", f.name); rr.Code != http.StatusConflict || errorCodeOf(rr) != "no_owner_keys" {
		t.Errorf("no owner keys listed = %d %s, want 409 no_owner_keys", rr.Code, rr.Body)
	}
	if rr := askAsOwner(t, ownerHandler(t, f, owner), owner, "become-root", ""); rr.Code != http.StatusUnprocessableEntity && rr.Code != http.StatusBadRequest {
		t.Errorf("an unknown purpose = %d %s, want a validation error", rr.Code, rr.Body)
	}
	if rr := askAsOwner(t, ownerHandler(t, f, owner), owner, "reset-admin", ""); rr.Code != http.StatusUnprocessableEntity && rr.Code != http.StatusBadRequest {
		t.Errorf("reset without a username = %d %s, want a validation error", rr.Code, rr.Body)
	}
}
