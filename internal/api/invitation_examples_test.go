package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/restmail/restmail"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const invitationExampleToken = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
const invitationExamplePassword = "the recipient chooses this password"

// A small transactional SQL ledger controls storage only. The production
// routers, scoped authorization, token hashing/spending, password repository,
// setup restrictions, JWT signing and browser cookies all run unchanged.
// PostgreSQL's row locks are tested separately in bootstrap's database tests.
type invitationLedger struct {
	token     models.OneTimeToken
	epoch     int64
	changed   *time.Time
	twoFactor bool
	password  string
	refresh   models.RefreshToken
}
type invitationConnection struct {
	capabilityExampleConnection
	state   invitationLedger
	mode    string
	queries int
	failed  bool
}

func (c *invitationConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }
func (c *invitationConnection) Driver() driver.Driver                        { return c }
func (c *invitationConnection) Open(string) (driver.Conn, error)             { return c, nil }

type invitationTransaction struct {
	c      *invitationConnection
	before invitationLedger
}

func (t invitationTransaction) Commit() error   { return nil }
func (t invitationTransaction) Rollback() error { t.c.state = t.before; return nil }
func (c *invitationConnection) Begin() (driver.Tx, error) {
	return invitationTransaction{c, c.state}, nil
}
func invitationFields(q string, args []driver.NamedValue) (map[string]any, error) {
	start := strings.Index(q, "(")
	end := strings.Index(q, ")")
	if start < 0 || end < start {
		return nil, errors.New("missing insert columns")
	}
	fields := strings.Split(q[start+1:end], ",")
	if len(fields) > len(args) {
		return nil, errors.New("missing insert values")
	}
	out := map[string]any{}
	for i, key := range fields {
		out[strings.Trim(key, `" `)] = args[i].Value
	}
	return out, nil
}
func invitationRows(columns []string, rows ...[]driver.Value) driver.Rows {
	return &authReadSnapshotRows{columns: columns, rows: rows}
}
func (c *invitationConnection) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.queries++
	if strings.Contains(q, `FROM "mailboxes"`) {
		if c.mode == "storage_failure" {
			return nil, errors.New("private account storage detail")
		}
		id := int64(12)
		for _, a := range args {
			if n, ok := a.Value.(int64); ok && (n == 1 || n == 12) {
				id = n
				break
			}
		}
		address := "invited@example.test"
		if id == 1 {
			address = "admin@example.test"
		}
		if strings.HasPrefix(q, `SELECT "address"`) {
			return invitationRows([]string{"address"}, []driver.Value{address}), nil
		}
		cols := []string{"id", "address", "display_name", "active", "credential_epoch", "password_change_required", "two_factor_required", "last_password_change"}
		if id == 12 && c.mode == "missing_account" {
			return invitationRows(cols), nil
		}
		var changed driver.Value
		if c.state.changed != nil {
			changed = *c.state.changed
		}
		return invitationRows(cols, []driver.Value{id, address, "", c.mode != "disabled_account", c.state.epoch, false, c.state.twoFactor, changed}), nil
	}
	if strings.Contains(q, "JOIN role_capabilities") {
		cols := []string{"scope", "capability", "expires_at", "activation_expires_at"}
		if c.mode == "denied" {
			return invitationRows(cols), nil
		}
		return invitationRows(cols, []driver.Value{"*", "mailboxes:write", nil, nil}), nil
	}
	if strings.Contains(q, `FROM "credentials"`) {
		if strings.HasPrefix(q, "SELECT count(*)") {
			n := int64(0)
			if c.state.password != "" || c.mode == "established_account" {
				n = 1
			}
			return invitationRows([]string{"count"}, []driver.Value{n}), nil
		}
		return invitationRows([]string{"id"}), nil // no old password or authenticator
	}
	if strings.Contains(q, `FROM "one_time_tokens"`) {
		token := c.state.token
		cols := []string{"id", "purpose", "token_hash", "account_id", "expires_at", "used_at"}
		if len(args) == 0 || args[0].Value != token.TokenHash {
			return invitationRows(cols), nil
		}
		if strings.Contains(q, "used_at IS NULL") && (token.UsedAt != nil || token.Purpose != models.OneTimeTokenInvitation || !token.ExpiresAt.After(time.Now())) {
			return invitationRows(cols), nil
		}
		var spent driver.Value
		if token.UsedAt != nil {
			spent = *token.UsedAt
		}
		return invitationRows(cols, []driver.Value{int64(21), token.Purpose, token.TokenHash, int64(12), token.ExpiresAt, spent}), nil
	}
	if strings.HasPrefix(q, `INSERT INTO "one_time_tokens"`) {
		if c.mode == "mint_failure" {
			c.failed = true
			return nil, errors.New("private token save detail")
		}
		f, err := invitationFields(q, args)
		if err != nil {
			return nil, err
		}
		if f["purpose"] != models.OneTimeTokenInvitation || f["account_id"] != int64(12) {
			return nil, errors.New("invitation insert lost purpose or account")
		}
		hash, _ := f["token_hash"].(string)
		expiry, _ := f["expires_at"].(time.Time)
		id := uint(12)
		c.state.token = models.OneTimeToken{ID: 21, Purpose: models.OneTimeTokenInvitation, TokenHash: hash, AccountID: &id, ExpiresAt: expiry}
		return invitationRows([]string{"id"}, []driver.Value{int64(21)}), nil
	}
	if strings.HasPrefix(q, `INSERT INTO "credentials"`) {
		if c.mode == "credential_failure" {
			c.failed = true
			return nil, errors.New("private credential save detail")
		}
		f, err := invitationFields(q, args)
		if err != nil {
			return nil, err
		}
		if f["account_id"] != int64(12) || f["kind"] != models.CredentialKindPassword || f["created_by_id"] != int64(12) {
			return nil, errors.New("password insert lost recipient")
		}
		c.state.password, _ = f["secret_hash"].(string)
		return invitationRows([]string{"id"}, []driver.Value{int64(31)}), nil
	}
	if strings.HasPrefix(q, `INSERT INTO "refresh_tokens"`) {
		f, err := invitationFields(q, args)
		if err != nil {
			return nil, err
		}
		if f["account_id"] != int64(12) || f["status"] != models.RefreshTokenActive {
			return nil, errors.New("session insert lost recipient")
		}
		c.state.refresh = models.RefreshToken{AccountID: 12, Status: models.RefreshTokenActive, Jti: f["jti"].(string), SessionID: f["session_id"].(string), ExpiresAt: f["expires_at"].(time.Time)}
		return invitationRows([]string{"id"}, []driver.Value{int64(41)}), nil
	}
	if strings.HasPrefix(q, `UPDATE "credentials"`) {
		return invitationRows([]string{"id"}), nil
	}
	return nil, fmt.Errorf("unexpected ancillary query: %s", q)
}
func (c *invitationConnection) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.queries++
	if strings.HasPrefix(q, `UPDATE "one_time_tokens"`) {
		if strings.Contains(q, "token_hash =") {
			if len(args) < 4 || args[1].Value != c.state.token.TokenHash || args[2].Value != models.OneTimeTokenInvitation || c.state.token.UsedAt != nil {
				return driver.RowsAffected(0), nil
			}
		} else if len(args) < 3 || args[1].Value != int64(12) || args[2].Value != models.OneTimeTokenInvitation {
			return nil, errors.New("replacement lost invitation scope")
		}
		now := time.Now()
		c.state.token.UsedAt = &now
		return driver.RowsAffected(1), nil
	}
	if strings.HasPrefix(q, `UPDATE "mailboxes"`) {
		if strings.Contains(q, "credential_epoch") {
			if c.mode == "account_update_failure" {
				c.failed = true
				return nil, errors.New("private account update detail")
			}
			c.state.epoch++
			c.state.twoFactor = true
			now := time.Now()
			c.state.changed = &now
		}
		return driver.RowsAffected(1), nil
	}
	return nil, errors.New("unexpected ancillary write")
}

type invitationFixture struct {
	db      *invitationConnection
	handler http.Handler
	jwt     *auth.JWTService
	token   string
}

func newInvitationFixture(t *testing.T, mode string) *invitationFixture {
	t.Helper()
	id := uint(12)
	sum := sha256.Sum256([]byte(invitationExampleToken))
	c := &invitationConnection{mode: mode, state: invitationLedger{token: models.OneTimeToken{ID: 21, Purpose: models.OneTimeTokenInvitation, TokenHash: hex.EncodeToString(sum[:]), AccountID: &id, ExpiresAt: time.Now().Add(10 * time.Minute)}}}
	if mode == "expired" {
		c.state.token.ExpiresAt = time.Now().Add(-time.Minute)
	}
	if mode == "wrong_purpose" {
		c.state.token.Purpose = models.OneTimeTokenResetAdmin
	}
	pool := sql.OpenDB(c)
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = pool.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}), &gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	jwt := auth.NewJWTService(routerTestSecret, 15*time.Minute, 7*24*time.Hour)
	session, err := jwt.GenerateSession(1, "admin@example.test", 0, []string{auth.AMRPassword}, false)
	if err != nil {
		t.Fatal("could not sign fixture session")
	}
	routers := NewRouters(db, jwt, &config.Config{Environment: "test", APIMaxBodyBytes: 512, TOTP2FAEnabled: mode != "unavailable", MasterKey: accessMasterKey}, nil)
	t.Cleanup(routers.Close)
	return &invitationFixture{c, routers.Public, jwt, session.AccessToken}
}
func (f *invitationFixture) request(t *testing.T, operation string, body any, authorized bool) *httptest.ResponseRecorder {
	t.Helper()
	path := "/api/v1/auth/invitation"
	if operation == "inviteAccount" {
		path = "/api/v1/admin/mailboxes/12/invitation"
	}
	var data []byte
	if body != nil {
		var err error
		data, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest("POST", path, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	if authorized {
		req.Header.Set("Authorization", "Bearer "+f.token)
	}
	rr := httptest.NewRecorder()
	f.handler.ServeHTTP(rr, req)
	return rr
}
func invitationBody(native bool) map[string]any {
	v := map[string]any{"token": invitationExampleToken, "password": invitationExamplePassword}
	if native {
		v["client"] = "native"
	}
	return v
}

func TestInvitationRealRoutesAndRollback(t *testing.T) {
	useRealGrants(t)
	t.Run("mint hash-only secret", func(t *testing.T) {
		f := newInvitationFixture(t, "")
		rr := f.request(t, "inviteAccount", nil, true)
		if rr.Code != 201 {
			t.Fatalf("mint=%d: %s", rr.Code, rr.Body.String())
		}
		var body struct {
			Data struct {
				Token     string
				ExpiresAt time.Time `json:"expires_at"`
			}
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		raw, err := base64.RawURLEncoding.DecodeString(body.Data.Token)
		sum := sha256.Sum256([]byte(body.Data.Token))
		if err != nil || len(raw) != 32 || hex.EncodeToString(sum[:]) != f.db.state.token.TokenHash || time.Until(body.Data.ExpiresAt) < 14*time.Minute || time.Until(body.Data.ExpiresAt) > 15*time.Minute {
			t.Fatal("unsafe invitation secret, hash or expiry")
		}
		if rr.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("secret response can be cached")
		}
	})
	for _, native := range []bool{false, true} {
		t.Run(fmt.Sprintf("accept native=%t", native), func(t *testing.T) {
			f := newInvitationFixture(t, "")
			rr := f.request(t, "acceptInvitation", invitationBody(native), false)
			if rr.Code != 200 {
				t.Fatalf("accept=%d: %s", rr.Code, rr.Body.String())
			}
			if f.db.state.token.UsedAt == nil || f.db.state.epoch != 1 || !f.db.state.twoFactor || f.db.state.changed == nil || auth.CheckPassword(invitationExamplePassword, f.db.state.password) != nil {
				t.Fatal("first password and token did not commit together")
			}
			var body struct {
				Data struct {
					AccessToken  string   `json:"access_token"`
					RefreshToken string   `json:"refresh_token"`
					Setup        []string `json:"setup_required"`
					Capabilities []string `json:"capabilities"`
				}
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(body.Data.Setup, []string{"two_factor"}) || len(body.Data.Capabilities) != 0 {
				t.Fatal("invitation bypassed setup")
			}
			access, refresh := body.Data.AccessToken, body.Data.RefreshToken
			if !native {
				if access != "" || refresh != "" {
					t.Fatal("browser body leaked session tokens")
				}
				for _, cookie := range rr.Result().Cookies() {
					if cookie.Name == auth.AccessCookieName {
						access = cookie.Value
					}
					if cookie.Name == auth.RefreshCookieName {
						refresh = cookie.Value
					}
					if (cookie.Name == auth.AccessCookieName || cookie.Name == auth.RefreshCookieName) && (!cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode) {
						t.Fatal("unsafe browser session cookie")
					}
				}
			} else if len(rr.Result().Cookies()) != 0 {
				t.Fatal("native login emitted browser cookies")
			}
			claims, err := f.jwt.ValidateAccessToken(access)
			if err != nil {
				t.Fatal("invitation access token is invalid")
			}
			if claims.AccountID != 12 || claims.CredentialEpoch != 1 || !claims.SetupPending || len(claims.AMR) != 0 {
				t.Fatal("invitation session has wrong principal or assurance")
			}
			rc, err := f.jwt.ValidateRefreshToken(refresh)
			if err != nil || rc.ID != f.db.state.refresh.Jti || rc.SessionID != f.db.state.refresh.SessionID {
				t.Fatal("refresh token is absent from rotation ledger")
			}
			replay := f.request(t, "acceptInvitation", invitationBody(native), false)
			if replay.Code != 401 {
				t.Fatal("spent invitation replay succeeded")
			}
		})
	}
	for _, mode := range []string{"credential_failure", "account_update_failure", "mint_failure"} {
		t.Run(mode, func(t *testing.T) {
			f := newInvitationFixture(t, mode)
			before := f.db.state
			op := "acceptInvitation"
			var body any = invitationBody(false)
			if mode == "mint_failure" {
				op = "inviteAccount"
				body = nil
			}
			rr := f.request(t, op, body, op == "inviteAccount")
			if rr.Code != 500 {
				t.Fatalf("failure=%d: %s", rr.Code, rr.Body.String())
			}
			if !f.db.failed {
				t.Fatal("injected storage failure was not reached")
			}
			if !reflect.DeepEqual(before, f.db.state) {
				t.Fatal("failed transaction changed token, account or credentials")
			}
			if strings.Contains(rr.Body.String(), "private") {
				t.Fatal("storage detail leaked")
			}
		})
	}
}

// Canonical pairs are replayed through these same production routes. Only the
// random issued token and expiry are normalized, after validating both above.
func TestInvitationCanonicalExamples(t *testing.T) {
	useRealGrants(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	recorded := oapifly.ExampleFile{}
	for _, tc := range []struct {
		op, name, mode string
		authorized     bool
	}{
		{"inviteAccount", "created", "", true}, {"inviteAccount", "unauthenticated", "", false}, {"inviteAccount", "insufficient_permissions", "denied", true},
		{"inviteAccount", "missing", "missing_account", true}, {"inviteAccount", "existing_credentials", "established_account", true}, {"inviteAccount", "unavailable", "unavailable", true},
		{"acceptInvitation", "invalid_token", "expired", false}, {"acceptInvitation", "wrong_purpose", "wrong_purpose", false}, {"acceptInvitation", "unavailable", "unavailable", false},
		{"acceptInvitation", "accepted", "", false}, {"acceptInvitation", "body_array", "", false}, {"acceptInvitation", "invalid_field", "", false}, {"acceptInvitation", "request_too_large", "", false},
		{"inviteAccount", "invalid_id", "", true}, {"inviteAccount", "request_too_large", "", true}, {"inviteAccount", "internal_error", "mint_failure", true},
		{"acceptInvitation", "internal_error", "credential_failure", false},
	} {
		t.Run(tc.op+"/"+tc.name, func(t *testing.T) {
			f := newInvitationFixture(t, tc.mode)
			var request any
			if tc.op == "acceptInvitation" {
				request = invitationBody(false)
			}
			if tc.name == "body_array" {
				request = []any{}
			}
			if tc.name == "invalid_field" {
				request = map[string]any{"token": 3, "password": invitationExamplePassword}
			}
			if tc.name == "request_too_large" {
				if tc.op == "acceptInvitation" {
					request = map[string]any{"token": invitationExampleToken, "password": strings.Repeat("x", 513)}
				}
			}
			path := "/api/v1/auth/invitation"
			if tc.op == "inviteAccount" {
				path = "/api/v1/admin/mailboxes/12/invitation"
			}
			if tc.name == "invalid_id" {
				path = "/api/v1/admin/mailboxes/0/invitation"
			}
			payload, _ := json.Marshal(request)
			if request == nil {
				payload = nil
			}
			req := httptest.NewRequest("POST", path, bytes.NewReader(payload))
			req.Header.Set("Content-Type", "application/json")
			if tc.name == "request_too_large" && tc.op == "inviteAccount" {
				// The endpoint has no typed body. Its transport cap still
				// refuses an oversized Content-Length before the handler.
				req.ContentLength = 513
			}
			if tc.authorized {
				req.Header.Set("Authorization", "Bearer "+f.token)
			}
			rr := httptest.NewRecorder()
			f.handler.ServeHTTP(rr, req)
			var body map[string]any
			if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if tc.name == "created" {
				data := body["data"].(map[string]any)
				data["token"] = invitationExampleToken
				data["expires_at"] = "2030-01-01T00:15:00Z"
			}
			if recorded[tc.op] == nil {
				recorded[tc.op] = map[string]oapifly.ExamplePair{}
			}
			pair := oapifly.ExamplePair{}
			pair.Request.Body = payload
			pair.Request.Invalid = rr.Code == 400 || rr.Code == 422
			if tc.op == "inviteAccount" {
				id := 12
				if tc.name == "invalid_id" {
					id = 0
				}
				pair.Request.Path = map[string]any{"id": id}
			}
			normalized, _ := json.Marshal(body)
			pair.Response.Status = rr.Code
			pair.Response.MediaType = "application/json"
			pair.Response.Body = normalized
			recorded[tc.op][tc.name] = pair
			if os.Getenv("RESTMAIL_INVITATION_RECORD") == "" {
				want, ok := pairs[tc.op][tc.name]
				if !ok {
					t.Fatal("canonical pair missing")
				}
				if want.Response.Status != rr.Code {
					t.Fatalf("got%d: %s", rr.Code, rr.Body.String())
				}
				assertPublicExampleBody(t, want.Response.Body, normalized)
				var a, b any
				_ = json.Unmarshal(want.Request.Body, &a)
				_ = json.Unmarshal(payload, &b)
				if !reflect.DeepEqual(a, b) {
					t.Fatal("canonical request differs from executed request")
				}
			}
		})
	}
	// Optional recording produces an artifact, never rewrites repository files.
	if path := os.Getenv("RESTMAIL_INVITATION_RECORD"); path != "" {
		data, err := oapifly.MarshalExamples(recorded)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
