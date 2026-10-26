package api

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/restmail/restmail"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
)

const deviceRemovalPath = "/api/v1/devices/{device_id}"

type deviceRemovalConnection struct {
	capabilityExampleConnection
	store         *bulkQueueExampleStore
	owner, second string
}

func (c deviceRemovalConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }

func (c deviceRemovalConnection) Driver() driver.Driver { return c }

func (c deviceRemovalConnection) Open(string) (driver.Conn, error) { return c, nil }

func (c deviceRemovalConnection) Begin() (driver.Tx, error) { return bulkQueueExampleTx{}, nil }

// The real store supplies both selectors. Fixed affected counts prove the
// session boundary wiring, not PostgreSQL/audit persistence or APNs delivery.
func (c deviceRemovalConnection) ExecContext(_ context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	if !strings.HasPrefix(q, `DELETE FROM "push_devices"`) || len(args) != 2 || !strings.Contains(q, "id =") || !strings.Contains(q, "session_id =") {
		return nil, errors.New("unexpected device mutation or lost session restriction")
	}
	c.store.mu.Lock()
	c.store.calls = append(c.store.calls, bulkQueueExampleMutation{q, append([]driver.NamedValue(nil), args...)})
	c.store.mu.Unlock()
	if args[0].Value == int64(3) {
		return nil, errors.New("private device deletion detail")
	}
	if args[0].Value == int64(1) && args[1].Value == c.owner || args[0].Value == int64(2) && args[1].Value == c.second {
		return driver.RowsAffected(1), nil
	}
	return driver.RowsAffected(0), nil
}

type deviceRemovalFixture struct {
	scenarios map[string]accountReadScenario
	store     *bulkQueueExampleStore
	sessions  map[string]string
}

func newDeviceRemovalFixture(t *testing.T) deviceRemovalFixture {
	t.Helper()
	unavailable, _, _, jwt := authReadBatchRouters(t)
	normal := mailboxToken(t, jwt)
	second, err := jwt.GenerateSession(10, "user@example.com", 0, []string{auth.AMRPassword}, false)
	if err != nil {
		t.Fatal(err)
	}
	other, err := jwt.GenerateSession(20, "other@example.com", 0, []string{auth.AMRPassword}, false)
	if err != nil {
		t.Fatal(err)
	}
	setup, err := jwt.GenerateSession(10, "user@example.com", 0, []string{auth.AMRPassword}, true)
	if err != nil {
		t.Fatal(err)
	}
	sid := func(token string) string {
		c, err := jwt.ValidateAccessToken(token)
		if err != nil {
			t.Fatal(err)
		}
		if c.SessionID == "" {
			t.Fatal("fixture lost real session identity")
		}
		return c.SessionID
	}
	store := &bulkQueueExampleStore{}
	pool := sql.OpenDB(deviceRemovalConnection{store: store, owner: sid(normal), second: sid(second.AccessToken)})
	t.Cleanup(func() { _ = pool.Close() })
	routers := NewRouters(openAccountReadDB(t, pool), jwt, &config.Config{Environment: "test", APIMaxBodyBytes: 8}, nil)
	t.Cleanup(routers.Close)
	out := map[string]accountReadScenario{}
	sessions := map[string]string{}
	for _, name := range []string{"found", "missing", "foreign_session", "leading_zero", "zero_id", "negative_id", "overflow_id", "invalid_id", "write_unavailable", "request_too_large"} {
		out[name] = accountReadScenario{routers.Public, normal}
		sessions[name] = sid(normal)
	}
	out["other_session"] = accountReadScenario{routers.Public, second.AccessToken}
	sessions["other_session"] = sid(second.AccessToken)
	out["own_second_session"] = out["other_session"]
	sessions["own_second_session"] = sid(second.AccessToken)
	out["other_account"] = accountReadScenario{routers.Public, other.AccessToken}
	sessions["other_account"] = sid(other.AccessToken)
	out["setup_required"] = accountReadScenario{routers.Public, setup.AccessToken}
	out["unauthenticated"] = accountReadScenario{routers.Public, ""}
	out["invalid_token"] = accountReadScenario{routers.Public, "invalid-token"}
	key, _, _, err := auth.NewAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	out["api_key"] = accountReadScenario{routers.Public, key}
	out["session_unavailable"] = accountReadScenario{unavailable, normal}
	password, _, _, err := auth.NewAppPassword()
	if err != nil {
		t.Fatal(err)
	}
	out["app_password"] = accountReadScenario{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.Clone(r.Context())
		r.SetBasicAuth("user@example.com", password)
		routers.Public.ServeHTTP(w, r)
	}), ""}
	for _, name := range []string{"cookie_session", "csrf_missing", "csrf_mismatch"} {
		name := name
		out[name] = accountReadScenario{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r = r.Clone(r.Context())
			r.Header.Del("Authorization")
			r.AddCookie(&http.Cookie{Name: auth.AccessCookieName, Value: normal})
			r.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: "device-csrf"})
			if name == "cookie_session" {
				r.Header.Set(auth.CSRFHeaderName, "device-csrf")
			}
			if name == "csrf_mismatch" {
				r.Header.Set(auth.CSRFHeaderName, "wrong-csrf")
			}
			routers.Public.ServeHTTP(w, r)
		}), ""}
		sessions[name] = sid(normal)
	}
	return deviceRemovalFixture{out, store, sessions}
}

func TestDeviceRemovalExamples(t *testing.T) {
	f := newDeviceRemovalFixture(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs["removePushDevice"]) < 20 {
		t.Fatal("incomplete device examples")
	}
	for name, pair := range pairs["removePushDevice"] {
		t.Run(name, func(t *testing.T) {
			s, ok := f.scenarios[name]
			if !ok {
				t.Fatalf("missing scenario %s", name)
			}
			before := len(f.store.snapshot())
			req := httptest.NewRequest(http.MethodDelete, messageCollectionReadURI(deviceRemovalPath, pair), nil)
			if s.token != "" {
				req.Header.Set("Authorization", "Bearer "+s.token)
			}
			if name == "request_too_large" {
				req.ContentLength = 9
			}
			rr := httptest.NewRecorder()
			s.handler.ServeHTTP(rr, req)
			if rr.Code != pair.Response.Status {
				t.Fatalf("got %d %s, want %d", rr.Code, rr.Body.String(), pair.Response.Status)
			}
			if rr.Code == 204 {
				if rr.Body.Len() != 0 || rr.Header().Get("Content-Type") != "" {
					t.Fatal("204 carried content")
				}
			} else {
				assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
			}
			calls := f.store.snapshot()[before:]
			wantWrite := rr.Code == 204 || name == "missing" || name == "foreign_session" || name == "other_session" || name == "other_account" || name == "write_unavailable"
			if (len(calls) == 1) != wantWrite || len(calls) > 1 {
				t.Fatalf("device deletion attempts=%d want %t", len(calls), wantWrite)
			}
			if wantWrite {
				id, err := strconv.ParseUint(pair.Request.Path["device_id"].(string), 10, 32)
				if err != nil {
					t.Fatal(err)
				}
				m := calls[0]
				if m.args[0].Value != int64(id) || m.args[1].Value != f.sessions[name] {
					t.Fatalf("device ID/session changed: %v", m.args)
				}
			}
		})
	}
	for _, g := range gapGenerators(t) {
		for _, w := range g.Report() {
			if w.OperationID == "removePushDevice" {
				t.Errorf("device removal warning: %v", w)
			}
		}
	}
}

func TestDeviceRemovalExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY for device exchanges")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	f := newDeviceRemovalFixture(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for name, pair := range pairs["removePushDevice"] {
		t.Run(name, func(t *testing.T) {
			s, ok := f.scenarios[name]
			if !ok {
				t.Fatalf("missing scenario %s", name)
			}
			checkQueueMutationPairWithVertrag(t, binary, bulkQueueExampleScenario{s, nil}, http.MethodDelete, deviceRemovalPath, name, pair)
		})
	}
}

func TestDeviceRemovalAbsentWhenPushDisabled(t *testing.T) {
	t.Setenv("RESTMAIL_APNS_KEY_FILE", "")
	t.Setenv("RESTMAIL_APNS_KEY_ID", "")
	t.Setenv("RESTMAIL_APNS_TEAM_ID", "")
	r := describedRouters(t)
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/devices/1", nil)
	rr := httptest.NewRecorder()
	r.Public.ServeHTTP(rr, req)
	if rr.Code != 404 {
		t.Fatalf("disabled push served device route: %d", rr.Code)
	}
	doc := contractDocument(t, r.generator("push disabled"))
	if _, ok := doc["paths"].(map[string]interface{})[deviceRemovalPath]; ok {
		t.Fatal("disabled push described device route")
	}
}
