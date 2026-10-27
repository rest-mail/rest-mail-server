package handlers

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/crypto"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/db/repositories"
)

// A second factor that could not be checked is the server's failure, not a
// wrong code (#1162). verifyTOTPOrRecovery used to drop the error from reading
// the authenticators, decrypting a secret or spending a recovery code. Every
// caller then refused the right code as an invalid one, and charged the
// sign-in and recovery throttles for the server's own fault.

var errSecondFactorStore = errors.New("second-factor store unavailable: dial tcp 10.0.0.9:5432")

// failingTwoFactorStore is a fakeTwoFactorStore whose authenticator read or
// recovery-code spend fails, while GetActive still finds the enrollment.
type failingTwoFactorStore struct {
	*fakeTwoFactorStore
	failActive  bool
	failConsume bool
}

func (f *failingTwoFactorStore) Active(accountID uint) ([]models.Credential, error) {
	if f.failActive {
		return nil, errSecondFactorStore
	}
	return f.fakeTwoFactorStore.Active(accountID)
}

func (f *failingTwoFactorStore) ConsumeRecoveryCode(accountID uint, plaintext string) (bool, error) {
	if f.failConsume {
		return false, errSecondFactorStore
	}
	return f.fakeTwoFactorStore.ConsumeRecoveryCode(accountID, plaintext)
}

// activeTwoFactor enrolls and confirms an authenticator for the account, and
// returns its secret and recovery codes.
func activeTwoFactor(t *testing.T, store twoFactorStore, accountID uint) enrollResponse {
	t.Helper()
	h := newTFHandler(store)
	c := mailboxClaims(accountID, "second-factor@example.test")
	resp := doEnroll(t, h, c)
	code, _ := auth.GenerateTOTPCode(resp.Secret, time.Now())
	if rr := postCode(t, h, "confirm", c, confirmRequest{TOTPCode: code}); rr.Code != http.StatusNoContent {
		t.Fatalf("confirm: %d %s", rr.Code, rr.Body.String())
	}
	return resp
}

func TestVerifyTOTPOrRecoveryReportsWhatItCouldNotCheck(t *testing.T) {
	store := newFakeStore()
	resp := activeTwoFactor(t, store, 1)
	code, _ := auth.GenerateTOTPCode(resp.Secret, time.Now())

	cases := []struct {
		name         string
		store        twoFactorStore
		masterKey    string
		totp, rescue string
		want         bool
		wantErr      error
	}{
		{"right code", store, testMasterKey, code, "", true, nil},
		{"wrong code", store, testMasterKey, "000000", "", false, nil},
		{"wrong recovery code", store, testMasterKey, "", "not-a-recovery-code", false, nil},
		{"authenticators unreadable", &failingTwoFactorStore{fakeTwoFactorStore: store, failActive: true},
			testMasterKey, code, "", false, errSecondFactorStore},
		{"recovery code cannot be spent", &failingTwoFactorStore{fakeTwoFactorStore: store, failConsume: true},
			testMasterKey, "", resp.RecoveryCodes[1], false, errSecondFactorStore},
		{"secret does not decrypt", store, "another-master-key-0123456789abcd", code, "", false, errTwoFactorUnreadable},
		{"no encryption key", store, "", code, "", false, errTwoFactorUnreadable},
	}
	for _, tc := range cases {
		got, err := verifyTOTPOrRecovery(tc.store, tc.masterKey, 1, tc.totp, tc.rescue)
		if got != tc.want || !errors.Is(err, tc.wantErr) || (tc.wantErr == nil) != (err == nil) {
			t.Errorf("%s: (%v, %v), want (%v, %v)", tc.name, got, err, tc.want, tc.wantErr)
		}
	}

	// One authenticator that does not decrypt does not hide another that
	// matches: the failure counts only when nothing matched.
	encrypted, err := crypto.EncryptString(resp.Secret, testMasterKey)
	if err != nil {
		t.Fatal(err)
	}
	confirmed := time.Now()
	second := newFakeStore()
	second.creds = []*models.Credential{
		{ID: 1, AccountID: 2, Kind: models.CredentialKindTOTP, EncryptedSecret: "not-a-ciphertext", ConfirmedAt: &confirmed},
		{ID: 2, AccountID: 2, Kind: models.CredentialKindTOTP, EncryptedSecret: encrypted, ConfirmedAt: &confirmed},
	}
	if got, err := verifyTOTPOrRecovery(second, testMasterKey, 2, code, ""); !got || err != nil {
		t.Errorf("a matching authenticator beside an unreadable one: (%v, %v), want (true, nil)", got, err)
	}
}

// Turning two-factor off with the right code while the authenticators cannot
// be read is a 500 that leaves 2FA on, not a 403 asking for another code.
func TestDisableTwoFactorReportsAStorageFailureNotAWrongCode(t *testing.T) {
	for _, tc := range []struct {
		name  string
		store func(*fakeTwoFactorStore) *failingTwoFactorStore
		body  func(enrollResponse) disableRequest
	}{
		{"authenticator code", func(f *fakeTwoFactorStore) *failingTwoFactorStore {
			return &failingTwoFactorStore{fakeTwoFactorStore: f, failActive: true}
		}, func(resp enrollResponse) disableRequest {
			code, _ := auth.GenerateTOTPCode(resp.Secret, time.Now())
			return disableRequest{TOTPCode: code}
		}},
		{"recovery code", func(f *fakeTwoFactorStore) *failingTwoFactorStore {
			return &failingTwoFactorStore{fakeTwoFactorStore: f, failConsume: true}
		}, func(resp enrollResponse) disableRequest {
			return disableRequest{RecoveryCode: resp.RecoveryCodes[0]}
		}},
	} {
		store := newFakeStore()
		resp := activeTwoFactor(t, store, 1)
		h := newTFHandler(tc.store(store))
		rr := postCode(t, h, "disable", mailboxClaims(1, "second-factor@example.test"), tc.body(resp))
		if rr.Code != http.StatusInternalServerError {
			t.Errorf("%s: %d %s, want 500", tc.name, rr.Code, rr.Body.String())
		}
		if strings.Contains(rr.Body.String(), "10.0.0.9") {
			t.Errorf("%s: the dependency failure reached the client: %s", tc.name, rr.Body.String())
		}
		if _, err := store.GetActive(1); err != nil {
			t.Errorf("%s: 2FA was turned off by a request whose code was never checked", tc.name)
		}
	}
}

// At sign-in, an authenticator read or recovery-code spend that fails is a
// 500, as an enrollment read that fails already was, and not a 401 wrong code.
func TestLoginEnforcementReportsAStorageFailureNotAWrongCode(t *testing.T) {
	store := newFakeStore()
	resp := activeTwoFactor(t, store, 7)
	code, _ := auth.GenerateTOTPCode(resp.Secret, time.Now())

	for name, req := range map[string]struct {
		store *failingTwoFactorStore
		login loginRequest
	}{
		"authenticator code": {&failingTwoFactorStore{fakeTwoFactorStore: store, failActive: true}, loginRequest{TOTPCode: code}},
		"recovery code":      {&failingTwoFactorStore{fakeTwoFactorStore: store, failConsume: true}, loginRequest{RecoveryCode: resp.RecoveryCodes[0]}},
	} {
		h := &AuthHandler{twoFactorStore: req.store, masterKey: testMasterKey}
		ok, rr := enforce(h, 7, req.login)
		if ok || rr.Code != http.StatusInternalServerError || errorCode(rr) != "internal_error" {
			t.Errorf("%s: ok=%v %d %q, want a 500 internal_error", name, ok, rr.Code, errorCode(rr))
		}
	}
	// The recovery code was not spent by the failed attempt.
	h := &AuthHandler{twoFactorStore: store, masterKey: testMasterKey}
	if ok, _ := enforce(h, 7, loginRequest{RecoveryCode: resp.RecoveryCodes[0]}); !ok {
		t.Error("the recovery code was spent by a sign-in that failed to check it")
	}
}

// Recovery with both factors right, while the authenticators cannot be read,
// is a 500 that resets nothing and charges nothing to the address.
func TestRecoverReportsAStorageFailureNotARefusal(t *testing.T) {
	gdb := openLoginTestDB(t)
	const masterKey = "recover-master-key-0123456789abcd"
	h := NewAuthHandler(gdb, auth.NewJWTService("recover-secret", 15*time.Minute, 7*24*time.Hour), masterKey, nil)
	addr := seedLoginMailbox(t, gdb, "correct-horse-battery")
	var mb models.Mailbox
	if err := gdb.Where("address = ?", addr).First(&mb).Error; err != nil {
		t.Fatalf("load mailbox: %v", err)
	}
	secret, codes := enrollWithRecoveryCodes(t, gdb, mb.ID, masterKey)
	real := repositories.NewTwoFactorRepository(gdb)
	h.twoFactorStore = &failingRepositoryStore{TwoFactorRepository: real, failActive: true}

	body := map[string]string{"email": addr, "totp_code": totpNow(t, secret), "recovery_code": codes[0],
		"new_password": "a-new-password-nobody-saw"}
	if rr := post(recoverHandler(h), body); rr.Code != http.StatusInternalServerError || errorCode(rr) != "internal_error" {
		t.Fatalf("recover with unreadable authenticators: %d %s, want 500 internal_error", rr.Code, rr.Body.String())
	}

	h.twoFactorStore = real
	body["totp_code"] = totpNow(t, secret)
	if rr := post(recoverHandler(h), body); rr.Code != http.StatusOK {
		t.Fatalf("recover once the store answers: %d %s, want 200 (nothing spent before)", rr.Code, rr.Body.String())
	}
}

// failingRepositoryStore is the real repository with its authenticator read
// failing.
type failingRepositoryStore struct {
	*repositories.TwoFactorRepository
	failActive bool
}

func (f *failingRepositoryStore) Active(accountID uint) ([]models.Credential, error) {
	if f.failActive {
		return nil, errSecondFactorStore
	}
	return f.TwoFactorRepository.Active(accountID)
}
