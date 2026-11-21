package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/db/repositories"
	"golang.org/x/crypto/bcrypt"
)

const credentialHardeningBcryptEnv = "RESTMAIL_TEST_PRODUCTION_BCRYPT"

func TestAC04CredentialHardeningWithoutLockout(t *testing.T) {
	t.Run("bcrypt", func(t *testing.T) {
		if os.Getenv(credentialHardeningBcryptEnv) == "1" {
			proveCredentialHardeningBcrypt(t)
			return
		}
		binary, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.CommandContext(t.Context(), binary, "-test.run=^TestAC04CredentialHardeningWithoutLockout$/^bcrypt$", "-test.v", "-test.timeout=30m")
		cmd.Env = append(os.Environ(), credentialHardeningBcryptEnv+"=1")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("production bcrypt proof: %v\n%s", err, output)
		}
		if strings.Contains(string(output), "--- SKIP:") {
			t.Skipf("production bcrypt proof skipped:\n%s", output)
		}
	})
	t.Run("controlled sign-in pacing", proveCredentialHardeningPacing)
	t.Run("gateway client allowance", func(t *testing.T) {
		for _, gateway := range []struct{ name, protocol string }{{imapCN, "imap"}, {pop3CN, "pop3"}, {submissionCN, "smtp"}} {
			t.Run(gateway.protocol, func(t *testing.T) {
				fixture := throttledGatewayFixture(t, true)
				fixture.internal = fixture.routers.Internal
				fixture.routers.signInTarpit.SetWait(func(context.Context, time.Duration) {})
				const busy = "198.51.100.77"
				for attempt := range signInBurst {
					rr := fixture.openFrom(gateway.name, gateway.protocol, "wrong app password", busy)
					if rr.Code != http.StatusUnauthorized {
						t.Fatalf("busy client failure %d: got %d %s, want 401", attempt+1, rr.Code, rr.Body)
					}
				}
				if rr := fixture.openFrom(gateway.name, gateway.protocol, fixture.app.Password, busy); rr.Code != http.StatusTooManyRequests {
					t.Errorf("busy client beyond burst: got %d %s, want 429", rr.Code, rr.Body)
				}
				for client := range signInBurst + 5 {
					ip := fmt.Sprintf("198.51.100.%d", client+1)
					rr := fixture.openFrom(gateway.name, gateway.protocol, fixture.app.Password, ip)
					if rr.Code != http.StatusCreated {
						t.Fatalf("fresh client %s: got %d %s, want 201", ip, rr.Code, rr.Body)
					}
				}
			})
		}
	})
}

func proveCredentialHardeningBcrypt(t *testing.T) {
	f := newGrantsFixture(t)
	const address = "bcrypt-proof@a.test"
	const password = "walrus-tundra-staple-7342"
	created := doRequest(f.router, http.MethodPost, "/api/v1/accounts", f.token(t, "owner"),
		fmt.Sprintf(`{"address":%q,"password":%q}`, address, password))
	if created.Code != http.StatusCreated {
		t.Fatalf("create password: got %d %s", created.Code, created.Body)
	}
	var mailbox models.Mailbox
	if err := f.db.Where("address = ?", address).First(&mailbox).Error; err != nil {
		t.Fatal(err)
	}
	assertCost := func(want int) {
		t.Helper()
		var credentials []models.Credential
		if err := f.db.Where("account_id = ? AND kind = ? AND revoked_at IS NULL", mailbox.ID, models.CredentialKindPassword).Find(&credentials).Error; err != nil {
			t.Fatal(err)
		}
		if len(credentials) != 1 {
			t.Fatalf("usable passwords: got %d, want one", len(credentials))
		}
		cost, err := bcrypt.Cost([]byte(strings.TrimPrefix(credentials[0].SecretHash, "{BLF-CRYPT}")))
		if err != nil || cost != want {
			t.Fatalf("stored bcrypt cost: got %d (%v), want %d", cost, err, want)
		}
	}
	assertCost(12)
	const legacyHash = "{BLF-CRYPT}$2y$08$FTAICgiE0XeQdtsHqQPtC.jWW6uCXM1I14vkJ70oIr2pX48AYUByC"
	if _, err := repositories.NewCredentialRepository(f.db).SetPassword(mailbox.ID, legacyHash, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	assertCost(8)
	for _, attempt := range []struct {
		password string
		status   int
		cost     int
	}{{"wrong password", http.StatusUnauthorized, 8}, {password, http.StatusOK, 12}, {password, http.StatusOK, 12}} {
		rr := doRequest(f.router, http.MethodPost, "/api/v1/auth/login", "",
			fmt.Sprintf(`{"email":%q,"password":%q,"client":"native"}`, address, attempt.password))
		if rr.Code != attempt.status {
			t.Fatalf("sign-in: got %d %s, want %d", rr.Code, rr.Body, attempt.status)
		}
		assertCost(attempt.cost)
	}
}

func proveCredentialHardeningPacing(t *testing.T) {
	useRealGrants(t)
	f := newCredentialFixtureWith(t, &config.Config{
		Environment: "test", AuthRateLimitEnabled: true, AuthRateLimitRPS: 100, AuthRateLimitBurst: 100, InternalMTLSEnabled: true,
	})
	var mailbox models.Mailbox
	if err := f.db.First(&mailbox, f.mailboxID).Error; err != nil {
		t.Fatal(err)
	}
	f.seedMailbox(mailbox.DomainID, "independent")
	const independent = "independent@example.test"
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	var waits []time.Duration
	f.routers.signInTarpit.SetClock(func() time.Time { return now })
	f.routers.signInTarpit.SetWait(func(_ context.Context, delay time.Duration) { waits = append(waits, delay) })
	check := func(address, ip, password string, gateway bool, status int, delay time.Duration) {
		t.Helper()
		waits = nil
		handler, path, body := f.router, "/api/v1/auth/login", fmt.Sprintf(`{"email":%q,"password":%q,"client":"native"}`, address, password)
		if gateway {
			handler, path, body = asGateway(imapCN, f.routers.Internal), "/api/v1/gateway-sessions",
				fmt.Sprintf(`{"protocol":"imap","address":%q,"password":%q,"client_ip":%q}`, address, password, ip)
		}
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = ip + ":40000"
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != status || (delay == 0 && len(waits) != 0) || (delay > 0 && (len(waits) != 1 || waits[0] != delay)) {
			t.Errorf("%s from %s: got %d %s with waits %v, want %d with delay %s", path, ip, rr.Code, rr.Body, waits, status, delay)
		}
	}
	const wrong = "incorrect fixture password"
	const busy = "198.51.100.77"
	for attempt := range 4 {
		delay := time.Duration(0)
		if attempt == 3 {
			delay = 500 * time.Millisecond
		}
		check(f.address, busy, wrong, true, http.StatusUnauthorized, delay)
	}
	check(f.address, "198.51.100.78", wrong, true, http.StatusUnauthorized, 0)
	for client, delay := range []time.Duration{250 * time.Millisecond, 500 * time.Millisecond, time.Second, 2 * time.Second, 3 * time.Second, 3 * time.Second, 3 * time.Second, 3 * time.Second, 3 * time.Second, 3 * time.Second, 3 * time.Second, 3 * time.Second} {
		check(f.address, fmt.Sprintf("198.51.100.%d", client+1), wrong, client%2 == 0, http.StatusUnauthorized, delay)
	}
	check(f.address, busy, f.password, false, http.StatusOK, 0)
	check(f.address, busy, f.password, true, http.StatusCreated, 0)
	check(f.address, busy, wrong, false, http.StatusUnauthorized, 3*time.Second)
	check(independent, busy, wrong, true, http.StatusUnauthorized, 0)
	check(independent, busy, f.password, false, http.StatusOK, 0)
	now = now.Add(time.Hour + time.Second)
	check(f.address, busy, wrong, false, http.StatusUnauthorized, 0)
}
