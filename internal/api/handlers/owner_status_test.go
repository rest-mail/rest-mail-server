package handlers

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/bootstrap"
	"github.com/restmail/restmail/internal/version"
	"golang.org/x/crypto/ssh"
)

// The status endpoint, end to end over HTTP: challenge, sign for "status", and
// the version the server runs.

// askForStatus does what `restmail status --key` does, signing for purpose
// and username (which it leaves empty), and returns the status response.
func askForStatus(t *testing.T, h *OwnerHandler, s ssh.Signer, purpose, username string) *httptest.ResponseRecorder {
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
	})
	rr = httptest.NewRecorder()
	h.Status(rr, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(b)))
	return rr
}

func TestStatus_AStatusKeyReadsWhatRuns(t *testing.T) {
	f := newInstalledServer(t)
	deployer := ownerKey(t)

	rr := askForStatus(t, keysHandler(t, f, nil, []ssh.Signer{deployer}), deployer, "status", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d %s", rr.Code, rr.Body)
	}
	var got struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"version":    version.Version,
		"commit":     version.Commit,
		"built_at":   version.BuildDate,
		"started_at": version.Started.UTC().Format(time.RFC3339),
	}
	if len(got.Data) != len(want) {
		t.Errorf("answered %v, want exactly %v: no secrets, no configuration", got.Data, want)
	}
	for k, v := range want {
		if got.Data[k] != v {
			t.Errorf("%s = %q, want %q", k, got.Data[k], v)
		}
	}
}

func TestStatus_AnOwnerKeyReadsItToo(t *testing.T) {
	f := newInstalledServer(t)
	owner := ownerKey(t)
	if rr := askForStatus(t, ownerHandler(t, f, owner), owner, "status", ""); rr.Code != http.StatusOK {
		t.Errorf("an owner key = %d %s, want 200", rr.Code, rr.Body)
	}
}

func TestStatus_Refusals(t *testing.T) {
	f := newInstalledServer(t)
	deployer, owner, stranger := ownerKey(t), ownerKey(t), ownerKey(t)
	h := keysHandler(t, f, []ssh.Signer{owner}, []ssh.Signer{deployer})

	if rr := askForStatus(t, h, stranger, "status", ""); rr.Code != http.StatusForbidden || errorCodeOf(rr) != "not_status_key" {
		t.Errorf("a stranger's key = %d %s, want 403 not_status_key", rr.Code, rr.Body)
	}
	// A signature made for something else does not read the status.
	if rr := askForStatus(t, h, owner, "reset-admin", f.name); rr.Code != http.StatusForbidden || errorCodeOf(rr) != "not_status_key" {
		t.Errorf("an owner's reset-admin signature = %d %s, want 403 not_status_key", rr.Code, rr.Body)
	}
	none := keysHandler(t, f, nil, nil)
	if rr := askForStatus(t, none, deployer, "status", ""); rr.Code != http.StatusConflict || errorCodeOf(rr) != "no_owner_keys" {
		t.Errorf("challenge with no keys listed = %d %s, want 409 no_owner_keys", rr.Code, rr.Body)
	}
	rr := httptest.NewRecorder()
	none.Status(rr, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader([]byte(`{"challenge":"x","public_key":"y","signature":""}`))))
	if rr.Code != http.StatusConflict || errorCodeOf(rr) != "no_status_keys" {
		t.Errorf("status with no keys listed = %d %s, want 409 no_status_keys", rr.Code, rr.Body)
	}
	rr = httptest.NewRecorder()
	h.Status(rr, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader([]byte(`{"signature":"not base64!"}`))))
	if rr.Code != http.StatusUnprocessableEntity && rr.Code != http.StatusBadRequest {
		t.Errorf("a signature that is not base64 = %d %s, want a validation error", rr.Code, rr.Body)
	}
}

// A status key reads the status, and nothing else.
func TestStatus_AStatusKeyCannotResetAnAdmin(t *testing.T) {
	f := newInstalledServer(t)
	deployer, owner := ownerKey(t), ownerKey(t)

	// Only status keys listed: a challenge is issued, but nothing can redeem it
	// for a reset.
	if rr := askAsOwner(t, keysHandler(t, f, nil, []ssh.Signer{deployer}), deployer, "reset-admin", f.name); rr.Code != http.StatusConflict || errorCodeOf(rr) != "no_owner_keys" {
		t.Errorf("reset-admin, only status keys listed = %d %s, want 409 no_owner_keys", rr.Code, rr.Body)
	}
	both := keysHandler(t, f, []ssh.Signer{owner}, []ssh.Signer{deployer})
	if rr := askAsOwner(t, both, deployer, "reset-admin", f.name); rr.Code != http.StatusForbidden || errorCodeOf(rr) != "not_owner" {
		t.Errorf("reset-admin with a status key = %d %s, want 403 not_owner", rr.Code, rr.Body)
	}
	if rr := askAsOwner(t, both, owner, "status", ""); rr.Code != http.StatusUnprocessableEntity || errorCodeOf(rr) != "validation_failed" {
		t.Errorf(`purpose "status" at the token endpoint = %d %s, want 422 validation_failed`, rr.Code, rr.Body)
	}
}
