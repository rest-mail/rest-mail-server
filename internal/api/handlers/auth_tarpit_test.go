package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/api/middleware"
	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/grants"
)

// tarpitWaits records the waits of a tarpit instead of sitting through them.
type tarpitWaits struct {
	mu    sync.Mutex
	waits []time.Duration
}

func (w *tarpitWaits) take() []time.Duration {
	w.mu.Lock()
	defer w.mu.Unlock()
	taken := w.waits
	w.waits = nil
	return taken
}

func recordedTarpit() (*middleware.SignInTarpit, *tarpitWaits) {
	tarpit, waits := middleware.NewSignInTarpit(), &tarpitWaits{}
	tarpit.SetWait(func(_ context.Context, d time.Duration) {
		waits.mu.Lock()
		defer waits.mu.Unlock()
		waits.waits = append(waits.waits, d)
	})
	return tarpit, waits
}

func loginFrom(h *AuthHandler, ip string, body map[string]string) *httptest.ResponseRecorder {
	data, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = ip + ":40000"
	rr := httptest.NewRecorder()
	serveAuth(h, rr, req)
	return rr
}

// Failures from many addresses against one account get slower and are never
// refused, and the correct password during all of it succeeds with no wait, from
// a new address or from one of the attacker's (#1643).
func TestLoginUnderDistributedAttack(t *testing.T) {
	gdb := openLoginTestDB(t)
	jwtSvc := auth.NewJWTService("login-secret", 15*time.Minute, 7*24*time.Hour)
	h := NewAuthHandler(gdb, jwtSvc, "", nil)
	tarpit, waits := recordedTarpit()
	h.SetSignInTarpit(tarpit)
	const password = "correct-horse-battery"
	addr := seedLoginMailbox(t, gdb, password)

	var last time.Duration
	for i := 0; i < 30; i++ {
		rr := loginFrom(h, fmt.Sprintf("198.51.100.%d", i+1), map[string]string{"email": addr, "password": "a wrong guess"})
		if rr.Code != http.StatusUnauthorized || loginErrorMessage(rr) != "Invalid email or password" {
			t.Fatalf("failure %d: %d %s: a failure is only ever the ordinary refusal", i+1, rr.Code, rr.Body.String())
		}
		var waited time.Duration
		if got := waits.take(); len(got) == 1 {
			waited = got[0]
		}
		if waited < last {
			t.Fatalf("failure %d waited %v, less than the one before's %v", i+1, waited, last)
		}
		last = waited
	}
	if last != 3*time.Second {
		t.Fatalf("after 30 failures the wait is %v, want the 3s cap", last)
	}

	for _, ip := range []string{"192.0.2.77", "198.51.100.1"} {
		rr := loginFrom(h, ip, map[string]string{"email": addr, "password": password})
		if rr.Code != http.StatusOK {
			t.Fatalf("the correct password from %s during the attack: %d %s", ip, rr.Code, rr.Body.String())
		}
		if got := waits.take(); len(got) != 0 {
			t.Errorf("the correct password from %s waited %v", ip, got)
		}
	}

	// It did not clear the count for the attacker.
	loginFrom(h, "198.51.100.200", map[string]string{"email": addr, "password": "a wrong guess"})
	if got := waits.take(); len(got) != 1 || got[0] != 3*time.Second {
		t.Errorf("a failure after the owner signed in waited %v, want the cap", got)
	}

	other := seedLoginMailbox(t, gdb, password)
	loginFrom(h, "198.51.100.1", map[string]string{"email": other, "password": "a wrong guess"})
	if got := waits.take(); len(got) != 0 {
		t.Errorf("another account waited %v because of this one's attack", got)
	}
}

// An address with no account is slowed exactly as one with an account is, so the
// wait says nothing about which exist.
func TestLoginDelaysAnUnknownAddressTheSame(t *testing.T) {
	gdb := openLoginTestDB(t)
	h := NewAuthHandler(gdb, auth.NewJWTService("login-secret", 15*time.Minute, 7*24*time.Hour), "", nil)
	tarpit, waits := recordedTarpit()
	h.SetSignInTarpit(tarpit)
	known := seedLoginMailbox(t, gdb, "correct-horse-battery")
	unknown := "nobody-" + strconv.FormatInt(time.Now().UnixNano(), 36) + "@example.test"

	for i := 0; i < 12; i++ {
		ip := fmt.Sprintf("198.51.100.%d", i+1)
		loginFrom(h, ip, map[string]string{"email": known, "password": "a wrong guess"})
		knownWait := waits.take()
		loginFrom(h, ip, map[string]string{"email": unknown, "password": "a wrong guess"})
		unknownWait := waits.take()
		if fmt.Sprint(knownWait) != fmt.Sprint(unknownWait) {
			t.Fatalf("failure %d: known address waited %v, unknown %v", i+1, knownWait, unknownWait)
		}
	}
}

// A wrong second-factor code waits as a wrong password does; being asked for a
// code does not, and neither does a correct one.
func TestLoginCountsAWrongSecondFactor(t *testing.T) {
	gdb := openLoginTestDB(t)
	const masterKey = "login-2fa-master-key-0123456789ab"
	h := NewAuthHandler(gdb, auth.NewJWTService("login-secret", 15*time.Minute, 7*24*time.Hour), masterKey, nil)
	const password = "correct-horse-battery"
	addr := seedLoginMailbox(t, gdb, password)
	var account models.Mailbox
	if err := gdb.Where("address = ?", addr).First(&account).Error; err != nil {
		t.Fatal(err)
	}
	secret := enroll2FAForMailbox(t, gdb, account.ID, masterKey)
	tarpit, waits := recordedTarpit()
	h.SetSignInTarpit(tarpit)

	asked := loginFrom(h, "198.51.100.1", map[string]string{"email": addr, "password": password})
	if asked.Code != http.StatusUnauthorized || errorCodeOf(asked) != "totp_required" {
		t.Fatalf("no code: %d %s", asked.Code, asked.Body.String())
	}
	for i := 0; i < 5; i++ {
		loginFrom(h, fmt.Sprintf("198.51.100.%d", i+1), map[string]string{"email": addr, "password": password, "totp_code": "000000"})
	}
	if got := waits.take(); len(got) != 0 {
		t.Fatalf("waited inside the free allowance: %v", got)
	}
	wrong := loginFrom(h, "198.51.100.9", map[string]string{"email": addr, "password": password, "totp_code": "000000"})
	if wrong.Code != http.StatusUnauthorized {
		t.Fatalf("wrong code: %d %s", wrong.Code, wrong.Body.String())
	}
	if got := waits.take(); len(got) != 1 {
		t.Errorf("the sixth wrong code waited %v, want one wait", got)
	}
	code, err := auth.GenerateTOTPCode(secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	right := loginFrom(h, "198.51.100.10", map[string]string{"email": addr, "password": password, "totp_code": code})
	if right.Code != http.StatusOK {
		t.Fatalf("right code: %d %s", right.Code, right.Body.String())
	}
	if got := waits.take(); len(got) != 0 {
		t.Errorf("the right code waited %v", got)
	}
}

// Linking an account takes its password, so it is not a way round the wait.
func TestLinkWaitsOnAWrongPassword(t *testing.T) {
	db := openAccountsTestDB(t)
	fx := seedAccountsFixture(t, db)
	h := NewAccountHandler(db, grants.NewCache(db, grants.CacheTTL), testMasterKey)
	tarpit, waits := recordedTarpit()
	h.SetSignInTarpit(tarpit)
	link := func(password string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		accountRoutes(h).ServeHTTP(rr, authzRequest(http.MethodPost, "/api/v1/accounts/link", fx.user.ID, nil,
			fmt.Sprintf(`{"address":%q,"password":%q}`, fx.admin.Address, password)))
		return rr
	}
	for i := 0; i < 5; i++ {
		if rr := link("not it"); rr.Code != http.StatusForbidden {
			t.Fatalf("wrong password: %d %s", rr.Code, rr.Body.String())
		}
	}
	if got := waits.take(); len(got) < 1 {
		t.Fatal("five wrong passwords from one client did not wait")
	}
	if rr := link(accountsPassword); rr.Code != http.StatusCreated {
		t.Fatalf("right password: %d %s", rr.Code, rr.Body.String())
	}
	if got := waits.take(); len(got) != 0 {
		t.Errorf("the right password waited %v", got)
	}
}
