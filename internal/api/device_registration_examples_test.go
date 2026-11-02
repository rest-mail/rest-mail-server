package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/rest-mail/rest-mail-server/internal/api/handlers"
	"github.com/rest-mail/rest-mail-server/internal/api/middleware"
	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/examplepairs"
	"github.com/rest-mail/rest-mail-server/internal/grants"
)

// registerPushDevice's pairs and accountEvents' invalid_id go through the
// production router with push configured (#636). The refusals need no
// database: one router's database fails, and another's session store does.
// The 201 and 200 run on a scratch database, and skip without one.

const deviceRegistrationPath = "/api/v1/devices"

func deviceRegistrationRouters(t *testing.T) (unavailable, failed http.Handler, jwt *auth.JWTService) {
	t.Helper()
	configureAuthReadPush(t)
	useRealGrants(t)
	jwt = auth.NewJWTService(routerTestSecret, 15*time.Minute, time.Hour)
	cfg := &config.Config{Environment: "test", APIMaxBodyBytes: requirementBodyCap}
	// Built before the seams are stood aside: its session check really reads
	// the failing database.
	sessionStore := NewRouters(newFailingGormDB(t), jwt, cfg, nil)
	t.Cleanup(sessionStore.Close)
	sessionCheck = func(*handlers.SessionCache) middleware.SessionCurrent {
		return func(uint, int) (bool, error) { return true, nil }
	}
	heldGrants = func(*grants.Cache) middleware.HeldGrants {
		return func(uint) (grants.Held, error) {
			return grants.HeldOf(grants.Entry{Capability: grants.Wildcard, Scope: grants.All}), nil
		}
	}
	database := NewRouters(newFailingGormDB(t), jwt, cfg, nil)
	t.Cleanup(database.Close)
	return sessionStore.Public, database.Public, jwt
}

// deviceRegistrationBody is the pair's request body as it goes on the wire:
// malformed_json's is the raw text its JSON string holds.
func deviceRegistrationBody(t *testing.T, pair oapifly.ExamplePair) []byte {
	t.Helper()
	body := []byte(pair.Request.Body)
	var raw string
	if pair.Requires["body"] == "malformed" {
		if err := json.Unmarshal(body, &raw); err != nil {
			t.Fatal(err)
		}
		return []byte(raw)
	}
	return body
}

func TestDeviceRegistrationRefusalExamples(t *testing.T) {
	unavailable, failed, jwt := deviceRegistrationRouters(t)
	token := mailboxToken(t, jwt)
	pending, err := jwt.GenerateSession(10, "user@example.com", 0, []string{auth.AMRPassword}, true)
	if err != nil {
		t.Fatal(err)
	}
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		token  string
		router http.Handler
	}{
		{"unauthenticated", "", failed},
		{"setup_required", pending.AccessToken, failed},
		{"malformed_json", token, failed},
		{"request_too_large", token, failed},
		// The handler refuses the app id before it looks anything up: the
		// failing database would answer 500.
		{"app_id_mismatch", token, failed},
		{"internal_error", token, failed},
		{"session_unavailable", token, unavailable},
	} {
		t.Run("registerPushDevice/"+tc.name, func(t *testing.T) {
			examplepairs.Proven(t, "registerPushDevice", tc.name)
			pair, ok := pairs["registerPushDevice"][tc.name]
			if !ok {
				t.Fatal("named response pair is missing")
			}
			req := httptest.NewRequest(http.MethodPost, deviceRegistrationPath, bytes.NewReader(deviceRegistrationBody(t, pair)))
			req.Header.Set("Content-Type", "application/json")
			if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}
			if tc.name == "request_too_large" {
				req.ContentLength = requirementBodyCap + 1
			}
			rr := httptest.NewRecorder()
			tc.router.ServeHTTP(rr, req)
			assertRequirementResponse(t, pair, rr)
		})
	}

	t.Run("accountEvents/setup_required", func(t *testing.T) {
		examplepairs.Proven(t, "accountEvents", "setup_required")
		pair := pairs["accountEvents"]["setup_required"]
		req := httptest.NewRequest(http.MethodGet, messageCollectionReadURI("/api/v1/accounts/{id}/events", pair), nil)
		req.Header.Set("Authorization", "Bearer "+pending.AccessToken)
		rr := httptest.NewRecorder()
		failed.ServeHTTP(rr, req)
		assertRequirementResponse(t, pair, rr)
	})

	t.Run("accountEvents/invalid_id", func(t *testing.T) {
		examplepairs.Proven(t, "accountEvents", "invalid_id")
		pair := pairs["accountEvents"]["invalid_id"]
		req := httptest.NewRequest(http.MethodGet, messageCollectionReadURI("/api/v1/accounts/{id}/events", pair), nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rr := httptest.NewRecorder()
		failed.ServeHTTP(rr, req)
		assertRequirementResponse(t, pair, rr)
	})
}

// sessionDevices are the devices stored for the fixture's session.
func sessionDevices(f *credentialFixture, sid string) []models.PushDevice {
	f.t.Helper()
	var devices []models.PushDevice
	if err := f.db.Where("session_id = ?", sid).Order("id").Find(&devices).Error; err != nil {
		f.t.Fatal(err)
	}
	return devices
}

// assertDeviceAnswer checks the answer names device, then compares the rest
// with the pair: the id and creation time are the database's.
func assertDeviceAnswer(t *testing.T, pair oapifly.ExamplePair, rr *httptest.ResponseRecorder, device models.PushDevice) {
	t.Helper()
	if rr.Code != pair.Response.Status || !strings.HasPrefix(rr.Header().Get("Content-Type"), pair.Response.MediaType) {
		t.Fatalf("got %d %q %s, want %d %q", rr.Code, rr.Header().Get("Content-Type"), rr.Body.String(), pair.Response.Status, pair.Response.MediaType)
	}
	var got, want map[string]map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(pair.Response.Body, &want); err != nil {
		t.Fatal(err)
	}
	if got["data"]["id"] != strconv.FormatUint(uint64(device.ID), 10) {
		t.Fatalf("answered device %v, stored %d", got["data"]["id"], device.ID)
	}
	created, err := time.Parse(time.RFC3339Nano, got["data"]["created_at"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if d := created.Sub(device.CreatedAt); d > time.Millisecond || d < -time.Millisecond {
		t.Fatalf("answered created_at %s, stored %s", created, device.CreatedAt)
	}
	got["data"]["id"], got["data"]["created_at"] = want["data"]["id"], want["data"]["created_at"]
	normalised, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	assertPublicExampleBody(t, pair.Response.Body, normalised)
}

func TestDeviceRegistrationExamples(t *testing.T) {
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	fixture := func(t *testing.T) (*credentialFixture, *auth.Claims, registerDeviceBody) {
		t.Helper()
		configureAuthReadPush(t)
		f := newCredentialFixtureWith(t, &config.Config{Environment: "test"})
		claims, err := f.jwt.ValidateAccessToken(strings.TrimPrefix(f.session, "Bearer "))
		if err != nil || claims.SessionID == "" {
			t.Fatalf("fixture session has no identity: %v", err)
		}
		// The fixture's session token has no ledger row, and a device registers
		// only on a session that is alive.
		alive := models.RefreshToken{Jti: claims.SessionID, AccountID: claims.AccountID, SessionID: claims.SessionID,
			Status: models.RefreshTokenActive, ExpiresAt: time.Now().Add(time.Hour)}
		if err := f.db.Create(&alive).Error; err != nil {
			t.Fatalf("seed the session: %v", err)
		}
		var body registerDeviceBody
		if err := json.Unmarshal(pairs["registerPushDevice"]["registered"].Request.Body, &body); err != nil {
			t.Fatal(err)
		}
		return f, claims, body
	}

	t.Run("registerPushDevice/registered", func(t *testing.T) {
		examplepairs.Proven(t, "registerPushDevice", "registered")
		pair := pairs["registerPushDevice"]["registered"]
		f, claims, body := fixture(t)
		rr := f.do(http.MethodPost, deviceRegistrationPath, f.session, string(pair.Request.Body))
		devices := sessionDevices(f, claims.SessionID)
		if len(devices) != 1 {
			t.Fatalf("%d devices stored for the session, want 1: %d %s", len(devices), rr.Code, rr.Body.String())
		}
		d := devices[0]
		if d.Token != body.Token || d.Platform != body.Platform || d.Environment != body.Environment || d.MailboxID != claims.AccountID {
			t.Fatalf("stored %+v, want the registration for account %d", d, claims.AccountID)
		}
		assertDeviceAnswer(t, pair, rr, d)
	})

	t.Run("registerPushDevice/already_registered", func(t *testing.T) {
		examplepairs.Proven(t, "registerPushDevice", "already_registered")
		pair := pairs["registerPushDevice"]["already_registered"]
		f, claims, body := fixture(t)
		// existing_device: this session registered the pair's token already.
		existing := models.PushDevice{SessionID: claims.SessionID, MailboxID: claims.AccountID, Platform: body.Platform, Token: body.Token, Environment: body.Environment}
		if err := f.db.Create(&existing).Error; err != nil {
			t.Fatalf("seed device: %v", err)
		}
		rr := f.do(http.MethodPost, deviceRegistrationPath, f.session, string(pair.Request.Body))
		devices := sessionDevices(f, claims.SessionID)
		if len(devices) != 1 || devices[0].ID != existing.ID {
			t.Fatalf("devices %+v after registering again, want only %d: %d %s", devices, existing.ID, rr.Code, rr.Body.String())
		}
		assertDeviceAnswer(t, pair, rr, devices[0])
	})
}

// registerDeviceBody is the registration the pairs send.
type registerDeviceBody struct {
	Platform    string `json:"platform"`
	Token       string `json:"token"`
	Environment string `json:"environment"`
}
