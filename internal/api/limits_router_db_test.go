package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/pipeline/filters"
	"github.com/rest-mail/rest-mail-server/internal/sendlimits"
)

// Limits from #660's table that the limits suite leaves unproven (#1166) and
// that live in the database, held here through the production router on a
// scratch database. The router's clocks are moved instead of waited on. They
// skip when no database is reachable.

// signIn signs address in with the fixture's password and returns its bearer
// authorization.
func (f *credentialFixture) signIn(address string) string {
	f.t.Helper()
	rr := f.do(http.MethodPost, "/api/v1/auth/login", "", fmt.Sprintf(`{"email":%q,"password":%q,"client":"native"}`, address, credentialTestPassword))
	if rr.Code != http.StatusOK {
		f.t.Fatalf("sign in as %s: %d %s", address, rr.Code, rr.Body.String())
	}
	var login struct {
		Data struct {
			AccessToken string `json:"access_token"`
		} `json:"data"`
	}
	decodeInto(f.t, rr, &login)
	return "Bearer " + login.Data.AccessToken
}

type sendAnswer struct {
	Data struct {
		Status      string    `json:"status"`
		LastError   string    `json:"last_error"`
		ScheduledAt time.Time `json:"scheduled_at"`
	} `json:"data"`
}

func (f *credentialFixture) send(authorization, from string) (*httptest.ResponseRecorder, sendAnswer) {
	f.t.Helper()
	rr := f.do(http.MethodPost, "/api/v1/messages/send", authorization,
		fmt.Sprintf(`{"from":%q,"to":["someone@example.org"],"subject":"s","body_text":"b"}`, from))
	var answer sendAnswer
	_ = json.Unmarshal(rr.Body.Bytes(), &answer)
	return rr, answer
}

// With the send limits the server loads by default (20 a minute, 100 an hour per
// account), an account's 100 sends in an hour go out, the 101st is held until the
// next hour by its account's hourly budget, another account still sends, and
// the held account sends again once the hour has turned.
func TestSends_DefaultHourlyAllowanceThroughTheRouter(t *testing.T) {
	// The defaults, not whatever this environment overrides; outside production
	// Load needs no secrets.
	t.Setenv("ENVIRONMENT", "test")
	for _, key := range []string{"API_SEND_RATE_LIMIT_PER_MINUTE", "API_SEND_RATE_LIMIT_PER_HOUR", "SEND_LIMIT_MESSAGES_PER_HOUR",
		"SEND_LIMIT_MESSAGES_PER_DAY", "SEND_LIMIT_RECIPIENTS_PER_HOUR", "SEND_LIMIT_RECIPIENTS_PER_DAY"} {
		t.Setenv(key, "")
	}
	loaded, err := config.Load()
	if err != nil {
		t.Fatalf("load the default config: %v", err)
	}
	if loaded.SendLimits == nil || loaded.SendLimits.Account.MessagesPerHour != 100 || loaded.SendLimits.PerMinute != 20 {
		t.Fatalf("the default send limits are not 20/min and 100/h: %+v", loaded.SendLimits)
	}
	f := newCredentialFixtureWith(t, &config.Config{
		Environment:                "test",
		APISendRateLimitPerMinute:  loaded.APISendRateLimitPerMinute,
		APISendRateLimitPerHour:    loaded.APISendRateLimitPerHour,
		APIMaxRecipientsPerMessage: loaded.APIMaxRecipientsPerMessage,
		SendLimits:                 loaded.SendLimits,
	})
	start := time.Date(2030, 1, 1, 10, 0, 0, 0, time.UTC)
	now := start
	f.routers.sendLimits.Now = func() time.Time { return now }
	// The default outbound pipeline holds the same allowance in its rate_limit
	// filter, on its own clock.
	t.Cleanup(filters.SetRateLimitClock(func() time.Time { return now }))

	// 20 a minute, so the minute's allowance is never what stops a send. The
	// filter's windows start at the first send in them, so each minute starts a
	// second after the last one ended.
	for i := 0; i < 100; i++ {
		now = start.Add(time.Duration(i/20) * (time.Minute + time.Second))
		if rr, _ := f.send(f.session, f.address); rr.Code != http.StatusCreated {
			t.Fatalf("send %d of the hour's 100: %d %s", i+1, rr.Code, rr.Body.String())
		}
	}

	now = start.Add(5 * (time.Minute + time.Second))
	rr, held := f.send(f.session, f.address)
	if rr.Code != http.StatusAccepted || held.Data.Status != "held" {
		t.Fatalf("the 101st send in the hour: %d %s, want 202 held", rr.Code, rr.Body.String())
	}
	if want := sendlimits.AccountScope(f.accountID) + ": messages_per_hour"; held.Data.LastError != want {
		t.Errorf("the 101st send held by %q, want %q", held.Data.LastError, want)
	}
	if !held.Data.ScheduledAt.Equal(start.Add(time.Hour)) {
		t.Errorf("the 101st send held until %s, want the next hour %s", held.Data.ScheduledAt, start.Add(time.Hour))
	}

	// Another account on the same server is unaffected.
	var chris models.Mailbox
	if err := f.db.First(&chris, f.mailboxID).Error; err != nil {
		t.Fatal(err)
	}
	f.seedMailbox(chris.DomainID, "dana")
	if rr, _ := f.send(f.signIn("dana@example.test"), "dana@example.test"); rr.Code != http.StatusCreated {
		t.Fatalf("another account's send after the first spent its hour: %d %s", rr.Code, rr.Body.String())
	}

	// The allowance comes back with the next hour.
	now = start.Add(time.Hour + time.Second)
	if rr, _ := f.send(f.session, f.address); rr.Code != http.StatusCreated {
		t.Fatalf("a send once the hour has turned: %d %s", rr.Code, rr.Body.String())
	}
}

// tlsrptReport is a report with n policy entries, each for example.test.
func tlsrptReport(n int) string {
	policy := `{"policy":{"policy-type":"sts","policy-domain":"example.test"},"summary":{"total-successful-session-count":1,"total-failure-session-count":0}}`
	return `{"organization-name":"Example Reporter","date-range":{"start-datetime":"2026-11-01T00:00:00Z","end-datetime":"2026-11-01T23:59:59Z"},"report-id":"r1","policies":[` +
		strings.TrimSuffix(strings.Repeat(policy+",", n), ",") + `]}`
}

// Through the production router a TLS-RPT report of 100 policy entries is
// stored whole, and one of 101 is refused with the validation answer, storing
// nothing.
func TestTLSRPT_PolicyCountThroughTheRouter(t *testing.T) {
	f := newCredentialFixture(t) // hosts example.test
	stored := func() int64 {
		var n int64
		if err := f.db.Model(&models.TLSReport{}).Count(&n).Error; err != nil {
			t.Fatal(err)
		}
		return n
	}
	post := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/.well-known/smtp-tlsrpt", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/tlsrpt+json")
		req.RemoteAddr = "192.0.2.20:25000"
		rr := httptest.NewRecorder()
		f.router.ServeHTTP(rr, req)
		return rr
	}

	rr := post(tlsrptReport(100))
	if rr.Code != http.StatusCreated || !strings.Contains(rr.Body.String(), `"stored":100`) {
		t.Fatalf("a report of 100 policies: %d %s, want 201 storing 100", rr.Code, rr.Body.String())
	}
	if n := stored(); n != 100 {
		t.Fatalf("a report of 100 policies stored %d rows", n)
	}

	rr = post(tlsrptReport(101))
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("a report of 101 policies: %d %s, want 422", rr.Code, rr.Body.String())
	}
	var refusal struct {
		Error struct {
			Code    string `json:"code"`
			Details struct {
				Fields map[string]string `json:"fields"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &refusal); err != nil {
		t.Fatalf("the 422 is not the error envelope: %v: %s", err, rr.Body.String())
	}
	if refusal.Error.Code != "validation_failed" || refusal.Error.Details.Fields["policies"] != "Must hold 1 to 100 policy entries" {
		t.Errorf("a report of 101 policies refused with %s, want validation_failed on policies", rr.Body.String())
	}
	if n := stored(); n != 100 {
		t.Errorf("the refused report stored rows: %d, want still 100", n)
	}
}

// Refused recoveries spend the address's budget of five; past it the router
// answers 429, and one more refusal is allowed only once the documented
// 15 minutes have passed.
func TestRecover_RefusalBudgetRefillsThroughTheRouter(t *testing.T) {
	f := newCredentialFixtureWith(t, &config.Config{
		Environment: "test",
		// Turns on the per-address budget. The per-IP throttle it comes with
		// has room for every request here, so only the address's budget runs out.
		AuthRateLimitEnabled: true,
		AuthRateLimitRPS:     0.0001,
		AuthRateLimitBurst:   100,
	})
	start := time.Date(2030, 1, 1, 10, 0, 0, 0, time.UTC)
	now := start
	f.routers.recoverFailures.SetClock(func() time.Time { return now })

	body := fmt.Sprintf(`{"email":%q,"totp_code":"000000","recovery_code":"nope","new_password":"a-new-password-nobody-saw"}`, "nobody-here@example.test")
	refused := func(when string) {
		t.Helper()
		rr := f.do(http.MethodPost, "/api/v1/auth/recover", "", body)
		if rr.Code != http.StatusUnauthorized || !strings.Contains(rr.Body.String(), `"code":"recovery_failed"`) {
			t.Fatalf("%s: %d %s, want 401 recovery_failed", when, rr.Code, rr.Body.String())
		}
	}
	throttled := func(when string) {
		t.Helper()
		rr := f.do(http.MethodPost, "/api/v1/auth/recover", "", body)
		if rr.Code != http.StatusTooManyRequests {
			t.Fatalf("%s: %d %s, want 429", when, rr.Code, rr.Body.String())
		}
		rateLimitedBody(t, rr, "Too many failed recoveries for this address; try again later")
	}

	for i := 0; i < recoveryBudget; i++ {
		refused(fmt.Sprintf("refusal %d within the budget", i+1))
	}
	throttled("past the budget")

	now = start.Add(15*time.Minute - time.Second)
	throttled("a second before the refill")

	now = start.Add(15 * time.Minute)
	refused("after the 15-minute refill")
	throttled("past the one refilled refusal")
}
