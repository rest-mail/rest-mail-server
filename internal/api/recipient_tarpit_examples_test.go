package api

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/examplepairs"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const recipientTarpitBase = 30 * time.Millisecond
const recipientTarpitMax = 75 * time.Millisecond

func recipientTarpitRouter(t *testing.T, enabled bool, base, max time.Duration) http.Handler {
	t.Helper()
	configureAuthReadPush(t)
	pool := sql.OpenDB(publicDiscoveryConnection{})
	t.Cleanup(func() {
		if err := pool.Close(); err != nil {
			t.Error(err)
		}
	})
	database, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}), &gorm.Config{
		DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatal(err)
	}
	routers := NewRouters(database, auth.NewJWTService(routerTestSecret, time.Minute, time.Hour), &config.Config{
		Environment: "test", GatewayHostname: "mx.example.test",
		RestmailTarpitEnabled: enabled, RestmailTarpitBase: base, RestmailTarpitMax: max,
	}, nil)
	t.Cleanup(routers.Close)
	return routers.Public
}

func recipientTarpitCases() []struct {
	name string
	wait time.Duration
} {
	return []struct {
		name string
		wait time.Duration
	}{
		{"missing", recipientTarpitBase},
		{"found", 0},
		{"disabled", 2 * recipientTarpitBase},
		{"internal_error", 0},
		{"invalid_address", recipientTarpitMax},
		{"missing", recipientTarpitMax},
		{"missing_address", 0},
		{"empty_address", 0},
		{"canonical_address", 0},
		{"unicode_address", 0},
	}
}

func recipientTarpitPairs(t *testing.T) map[string]oapifly.ExamplePair {
	t.Helper()
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	return pairs["restmailCheckMailbox"]
}

func recipientTarpitExampleRouter(t *testing.T, enabled bool) http.Handler {
	t.Helper()
	if !enabled {
		return recipientTarpitRouter(t, false, 3*time.Second, 3*time.Second)
	}
	return recipientTarpitRouter(t, true, recipientTarpitBase, recipientTarpitMax)
}

func assertRecipientTarpitWait(t *testing.T, enabled bool, elapsed, minimum time.Duration) {
	t.Helper()
	if enabled && elapsed < minimum {
		t.Fatalf("recipient-check took %s, want at least %s", elapsed, minimum)
	}
	if !enabled && elapsed >= 2*time.Second {
		t.Fatalf("disabled tarpit still slept: %s", elapsed)
	}
}

func TestRestmailRecipientTarpitExamples(t *testing.T) {
	pairs := recipientTarpitPairs(t)
	for _, enabled := range []bool{true, false} {
		name := "disabled"
		if enabled {
			name = "enabled"
		}
		t.Run(name, func(t *testing.T) {
			handler := recipientTarpitExampleRouter(t, enabled)
			for _, scenario := range recipientTarpitCases() {
				t.Run(scenario.name, func(t *testing.T) {
					pair, ok := pairs[scenario.name]
					if !ok {
						t.Fatal("missing recipient-check example")
					}
					request := httptest.NewRequest(http.MethodGet, publicDiscoveryURI("/restmail/mailboxes", pair), nil)
					request.RemoteAddr = "192.0.2.1:1234"
					response := httptest.NewRecorder()
					started := time.Now()
					handler.ServeHTTP(response, request)
					elapsed := time.Since(started)
					if response.Code != pair.Response.Status || strings.Split(response.Header().Get("Content-Type"), ";")[0] != pair.Response.MediaType {
						t.Fatalf("response = %d %s, want %d %s: %s", response.Code, response.Header().Get("Content-Type"), pair.Response.Status, pair.Response.MediaType, response.Body)
					}
					assertPublicExampleBody(t, pair.Response.Body, response.Body.Bytes())
					assertRecipientTarpitWait(t, enabled, elapsed, scenario.wait)
					examplepairs.Proven(t, "restmailCheckMailbox", scenario.name)
				})
			}
		})
	}
}

func checkRestmailRecipientTarpitExamplesWithVertrag(t *testing.T, binary string) {
	t.Helper()
	pairs := recipientTarpitPairs(t)
	for _, enabled := range []bool{true, false} {
		name := "disabled"
		if enabled {
			name = "enabled"
		}
		t.Run(name, func(t *testing.T) {
			router := recipientTarpitExampleRouter(t, enabled)
			observed := make(chan time.Duration, 1)
			handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				started := time.Now()
				router.ServeHTTP(writer, request)
				observed <- time.Since(started)
			})
			for _, scenario := range recipientTarpitCases() {
				t.Run(scenario.name, func(t *testing.T) {
					pair, ok := pairs[scenario.name]
					if !ok {
						t.Fatal("missing recipient-check example")
					}
					checkMailboxLookupWithVertrag(t, binary, handler, "", "/restmail/mailboxes", scenario.name, pair)
					elapsed := <-observed
					assertRecipientTarpitWait(t, enabled, elapsed, scenario.wait)
				})
			}
		})
	}
}

func TestRestmailRecipientTarpitCanceledRequest(t *testing.T) {
	handler := recipientTarpitRouter(t, true, 3*time.Second, 3*time.Second)
	request := httptest.NewRequest(http.MethodGet, "/restmail/mailboxes?address=not-an-address", nil)
	ctx, cancel := context.WithCancel(request.Context())
	cancel()
	response := httptest.NewRecorder()
	started := time.Now()
	handler.ServeHTTP(response, request.WithContext(ctx))
	if elapsed := time.Since(started); elapsed >= 2*time.Second {
		t.Fatalf("canceled request still slept: %s", elapsed)
	}
	if response.Code != http.StatusOK || response.Body.String() != "{\"data\":{\"exists\":false}}\n" {
		t.Fatalf("canceled negative lookup = %d %s", response.Code, response.Body)
	}
}
