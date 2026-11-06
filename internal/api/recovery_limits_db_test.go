package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/api/limittest"
	"github.com/rest-mail/rest-mail-server/internal/config"
)

// The per-address recovery budgets are built by the production router, five
// per address and then one per 15 minutes, so these tests send their requests
// through it, on a scratch database. A test lasts far less than the 15 minutes
// one more request takes to accrue, so the budget cannot refill during it.
// They skip when no database is reachable.

const recoveryBudget = 5

// rateLimitedBody fails t unless rr is the 429 rate_limited answer, with its
// message and a whole-second Retry-After.
func rateLimitedBody(t *testing.T, rr *httptest.ResponseRecorder, message string) {
	t.Helper()
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("a request past the budget: %d %s, want 429", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, `"code":"rate_limited"`) || !strings.Contains(body, message) {
		t.Errorf("the 429 body %s, want code rate_limited and message %q", body, message)
	}
	limittest.RetryAfter(t, rr.Header())
}

// Refused recoveries are charged to the address, known or not; past its
// budget the production router answers 429 without checking the factors.
func TestRecover_RefusalsPastTheAddressBudgetAreThrottled(t *testing.T) {
	f := newCredentialFixtureWith(t, &config.Config{
		Environment: "test",
		// Turns on the per-address budget. The per-IP throttle it comes with
		// has room for every request here, so only the address's budget runs out.
		AuthRateLimitEnabled: true,
		AuthRateLimitRPS:     0.0001,
		AuthRateLimitBurst:   100,
	})
	body := fmt.Sprintf(`{"email":%q,"totp_code":"000000","recovery_code":"nope","new_password":"a-new-quokka-sundial-unseen"}`, "nobody-here@example.test")
	for i := 0; i < recoveryBudget; i++ {
		rr := f.do(http.MethodPost, "/api/v1/auth/recover", "", body)
		if rr.Code != http.StatusUnauthorized || !strings.Contains(rr.Body.String(), `"code":"recovery_failed"`) {
			t.Fatalf("refusal %d within the budget: %d %s, want 401 recovery_failed", i+1, rr.Code, rr.Body.String())
		}
	}
	rr := f.do(http.MethodPost, "/api/v1/auth/recover", "", body)
	rateLimitedBody(t, rr, "Too many failed recoveries for this address; try again later")
}

// Every request for a recovery link is charged to the address, answered or
// not; past its budget the production router answers 429.
func TestRecoveryEmail_RequestsPastTheAddressBudgetAreThrottled(t *testing.T) {
	// The per-IP throttle is off, so only the address's budget runs out.
	f := newRecoveryExampleFixture(t)
	body := fmt.Sprintf(`{"email":%q}`, f.address)
	for i := 0; i < recoveryBudget; i++ {
		rr := f.do(http.MethodPost, "/api/v1/auth/recover/email", "", body)
		if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "If this account has a verified recovery email, a recovery link will be sent.") {
			t.Fatalf("request %d within the budget: %d %s, want 200 with the private answer", i+1, rr.Code, rr.Body.String())
		}
	}
	rr := f.do(http.MethodPost, "/api/v1/auth/recover/email", "", body)
	rateLimitedBody(t, rr, "Too many recovery requests for this address; try again later")
}
