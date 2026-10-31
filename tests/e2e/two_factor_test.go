package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/auth"
)

// Making accounts and credentials, linking accounts and raising quotas take a
// session signed in with a second factor, and installing a credential recent
// proof of one. The seeded admin has none, so the suite does these as the
// contract run's two-factor administrator: a superadmin as the seeded admin is,
// made by `chore test:contract-mfa-admin` and enrolled in TOTP through the API
// (tests/contract/mfa_admin.py). It signs in here with a real code from the
// authenticator that enrollment kept; nothing is forged.
const (
	twoFactorAdminAddress = "contract-mfa-admin@restmail.test"
	// A disposable test fixture password (tests/contract/fixtures prepareMFAAdmin).
	twoFactorAdminPassword = "contract-mfa-admin123!@"
	// A session is renewed well inside the 15-minute window for recent proof.
	twoFactorSessionLife = 10 * time.Minute
)

// The enrollment mfa_admin.py keeps, shared with the contract hooks: the
// authenticator's secret, the recovery codes left, and the last TOTP step any
// sign-in used, since the server refuses a code used before.
type twoFactorEnrollment struct {
	Secret        string   `json:"secret"`
	RecoveryCodes []string `json:"recovery_codes"`
	Step          int64    `json:"step"`
}

var (
	twoFactorAdminMu    sync.Mutex
	twoFactorAdminToken string
	twoFactorAdminAt    time.Time
)

// twoFactorEnrollmentPath is CONTRACT_MFA_ADMIN, as mfa_admin.py reads it, or
// .cache/contract/mfa-admin.json at the repository root.
func twoFactorEnrollmentPath() (string, error) {
	if path := os.Getenv("CONTRACT_MFA_ADMIN"); path != "" {
		return path, nil
	}
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, ".cache/contract/mfa-admin.json"), nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no go.mod above the working directory")
		}
		dir = parent
	}
}

// nextTOTPCode waits for a TOTP step no sign-in has used, and returns its code.
func nextTOTPCode(secret string, used int64) (string, int64, error) {
	for time.Now().Unix()/30 <= used {
		time.Sleep(time.Second)
	}
	now := time.Now()
	code, err := auth.GenerateTOTPCode(secret, now)
	return code, now.Unix() / 30, err
}

// signInWithCode signs in with a password and a TOTP code, as a native client
// does, and returns the access token.
func signInWithCode(address, password, code string) (string, error) {
	resp, err := newAPIClient().post("/api/v1/auth/login", map[string]string{
		"email": address, "password": password, "client": "native", "totp_code": code,
	})
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		Data struct {
			AccessToken string `json:"access_token"`
		} `json:"data"`
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("sign in %s with a code: %d: %s", address, resp.StatusCode, readBody(resp))
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.Data.AccessToken == "" {
		return "", fmt.Errorf("sign in %s with a code: no access token (%v)", address, err)
	}
	return out.Data.AccessToken, nil
}

// twoFactorAdmin is a client signed in as the two-factor administrator.
func twoFactorAdmin(t *testing.T) *apiClient {
	t.Helper()
	twoFactorAdminMu.Lock()
	defer twoFactorAdminMu.Unlock()
	if twoFactorAdminToken == "" || time.Since(twoFactorAdminAt) > twoFactorSessionLife {
		path, err := twoFactorEnrollmentPath()
		requireNoError(t, err)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("the two-factor administrator is not enrolled (chore test:contract-mfa-admin): %v", err)
		}
		var enrollment twoFactorEnrollment
		requireNoError(t, json.Unmarshal(raw, &enrollment))
		code, step, err := nextTOTPCode(enrollment.Secret, enrollment.Step)
		requireNoError(t, err)
		token, err := signInWithCode(twoFactorAdminAddress, twoFactorAdminPassword, code)
		requireNoError(t, err)
		enrollment.Step = step
		raw, err = json.Marshal(enrollment)
		requireNoError(t, err)
		requireNoError(t, os.WriteFile(path, raw, 0o600))
		twoFactorAdminToken, twoFactorAdminAt = token, time.Now()
	}
	return &apiClient{baseURL: apiBaseURL, token: twoFactorAdminToken}
}

// withTwoFactor enrolls an account in TOTP through the API, as its owner would,
// and returns a client signed in with a code from it. The account's own
// changes, such as making its app passwords or linking another account, take
// that. Enrolling is a change to the account: use an account of the test's own.
func withTwoFactor(t *testing.T, address, password string) *apiClient {
	t.Helper()
	plain := newAPIClient()
	requireNoError(t, plain.rawLogin(address, password))
	resp, err := plain.post("/api/v1/auth/2fa/enroll", map[string]string{})
	requireNoError(t, err)
	var enrolled struct {
		Data struct {
			Secret string `json:"secret"`
		} `json:"data"`
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("enroll %s in TOTP: %d: %s", address, resp.StatusCode, readBody(resp))
	}
	requireNoError(t, decodeJSON(resp, &enrolled))
	code, step, err := nextTOTPCode(enrolled.Data.Secret, 0)
	requireNoError(t, err)
	resp, err = plain.post("/api/v1/auth/2fa/confirm", map[string]string{"totp_code": code})
	requireNoError(t, err)
	if resp.StatusCode/100 != 2 {
		t.Fatalf("confirm %s's authenticator: %d: %s", address, resp.StatusCode, readBody(resp))
	}
	resp.Body.Close()
	code, _, err = nextTOTPCode(enrolled.Data.Secret, step)
	requireNoError(t, err)
	token, err := signInWithCode(address, password, code)
	requireNoError(t, err)
	return &apiClient{baseURL: apiBaseURL, token: token}
}
