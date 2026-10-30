package api

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// The ledger controls storage only. Public routing, binding, scoped grants,
// credential ceilings, import code, transactions and audit hashing stay real.
// PostgreSQL proves advisory-lock isolation separately; this is no lock oracle.
type importLedger struct {
	messages  []map[string]any
	auditSeq  int64
	auditHash string
	quota     int64
}

type importConnection struct {
	mode      string
	state     importLedger
	queries   []string
	failed    bool
	keyPublic string
	keyHash   string
}

func (c *importConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }
func (c *importConnection) Driver() driver.Driver                        { return c }
func (c *importConnection) Open(string) (driver.Conn, error)             { return c, nil }
func (c *importConnection) Close() error                                 { return nil }
func (c *importConnection) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepared statement")
}

type importTransaction struct {
	c      *importConnection
	before importLedger
}

func (t importTransaction) Commit() error   { return nil }
func (t importTransaction) Rollback() error { t.c.state = t.before; return nil }
func (c *importConnection) Begin() (driver.Tx, error) {
	before := c.state
	before.messages = append([]map[string]any(nil), c.state.messages...)
	return importTransaction{c, before}, nil
}
func (c *importConnection) BeginTx(ctx context.Context, _ driver.TxOptions) (driver.Tx, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return c.Begin()
}

func importRows(fields ...map[string]any) driver.Rows {
	if len(fields) == 0 {
		return invitationRows([]string{"id"})
	}
	columns := make([]string, 0, len(fields[0]))
	for name := range fields[0] {
		columns = append(columns, name)
	}
	sort.Strings(columns)
	var rows [][]driver.Value
	for _, field := range fields {
		values := make([]driver.Value, len(columns))
		for i, name := range columns {
			values[i] = field[name]
		}
		rows = append(rows, values)
	}
	return invitationRows(columns, rows...)
}

func (c *importConnection) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.queries = append(c.queries, q)
	if strings.Contains(q, `FROM "credentials"`) {
		if len(args) == 0 || args[0].Value != c.keyPublic {
			return invitationRows([]string{"id"}), nil
		}
		scope := "domain:example.test"
		if c.mode == "key_ceiling" {
			scope = "domain:other.test"
		}
		permissions, _ := json.Marshal(map[string]any{"permissions": []map[string]string{{"capability": "mailboxes:write", "scope": scope}}})
		return importRows(map[string]any{"id": int64(31), "account_id": int64(1), "kind": "api_key", "public_id": c.keyPublic, "secret_hash": c.keyHash, "scope": string(permissions), "revoked_at": nil, "expires_at": nil}), nil
	}
	if strings.HasPrefix(q, "WITH RECURSIVE owned") {
		// The fixture's mailboxes own no accounts: the subtree's usage is the mailbox's.
		used := c.state.quota
		if c.mode == "full" {
			used = 1 << 40
		}
		return invitationRows([]string{"coalesce"}, []driver.Value{used}), nil
	}
	if strings.Contains(q, `FROM "mailboxes"`) {
		if c.mode == "scope_failure" {
			c.failed = true
			return nil, errors.New("private mailbox storage failure")
		}
		id := int64(12)
		if len(args) != 0 {
			if n, ok := args[0].Value.(int64); ok {
				id = n
			}
		}
		if id != 1 && c.mode == "missing" {
			return invitationRows([]string{"id", "address"}), nil
		}
		address := "import@example.test"
		if id == 1 {
			address = "admin@example.test"
		}
		if id != 1 && c.mode == "disappeared" && !strings.HasPrefix(q, `SELECT "address"`) {
			return invitationRows([]string{"id", "address"}), nil
		}
		if strings.HasPrefix(q, `SELECT "address"`) {
			return invitationRows([]string{"address"}, []driver.Value{address}), nil
		}
		return importRows(map[string]any{"id": id, "address": address, "active": true, "credential_epoch": int64(0), "password_change_required": false, "two_factor_required": false, "quota_bytes": int64(1 << 20), "quota_used_bytes": c.state.quota}), nil
	}
	if strings.Contains(q, "JOIN role_capabilities") {
		columns := []string{"scope", "capability", "expires_at", "activation_expires_at"}
		if c.mode == "denied" {
			return invitationRows(columns), nil
		}
		scope := "domain:example.test"
		if c.mode == "global" {
			scope = "*"
		}
		if c.mode == "read_source" {
			return invitationRows(columns, []driver.Value{scope, "mailboxes:write", nil, nil}, []driver.Value{"*", "mail:read", nil, nil}), nil
		}
		return invitationRows(columns, []driver.Value{scope, "mailboxes:write", nil, nil}), nil
	}
	if strings.Contains(q, `FROM "messages"`) {
		if c.mode == "read_failure" {
			c.failed = true
			return nil, errors.New("private message lookup failure")
		}
		for _, message := range c.state.messages {
			if strings.Contains(q, "mailbox_id =") {
				if len(args) > 2 && message["mailbox_id"] == args[0].Value && message["folder"] == args[1].Value && message["message_id"] == args[2].Value {
					return importRows(message), nil
				}
			} else if len(args) > 0 && message["id"] == args[0].Value {
				return importRows(message), nil
			}
		}
		return invitationRows([]string{"id"}), nil
	}
	if strings.HasPrefix(q, `INSERT INTO "messages"`) {
		if c.mode == "insert_failure" {
			c.failed = true
			return nil, errors.New("private message insert failure")
		}
		fields, err := invitationFields(q, args)
		if err != nil {
			return nil, err
		}
		fields["id"] = int64(41 + len(c.state.messages))
		c.state.messages = append(c.state.messages, fields)
		return invitationRows([]string{"id"}, []driver.Value{fields["id"]}), nil
	}
	if strings.Contains(q, "SELECT seq, hash FROM audit_log") {
		return invitationRows([]string{"seq", "hash"}, []driver.Value{c.state.auditSeq, c.state.auditHash}), nil
	}
	if strings.HasPrefix(q, `INSERT INTO "audit_log"`) {
		if c.mode == "audit_failure" {
			c.failed = true
			return nil, errors.New("private audit append failure")
		}
		fields, err := invitationFields(q, args)
		if err != nil {
			return nil, err
		}
		c.state.auditSeq = fields["seq"].(int64)
		c.state.auditHash = fields["hash"].(string)
		return invitationRows([]string{"seq"}, []driver.Value{c.state.auditSeq}), nil
	}
	return nil, fmt.Errorf("unexpected import query: %s", q)
}

func (c *importConnection) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.queries = append(c.queries, q)
	if strings.HasPrefix(q, "SAVEPOINT ") || strings.HasPrefix(q, "ROLLBACK TO SAVEPOINT ") || strings.HasPrefix(q, "SELECT pg_advisory_xact_lock(") {
		return driver.RowsAffected(1), nil
	}
	if strings.HasPrefix(q, `INSERT INTO "audit_log"`) {
		rows, err := c.QueryContext(ctx, q, args)
		if rows != nil {
			_ = rows.Close()
		}
		return driver.RowsAffected(1), err
	}
	if strings.HasPrefix(q, `UPDATE "credentials"`) {
		return driver.RowsAffected(1), nil
	}
	if strings.HasPrefix(q, `UPDATE "quota_usage"`) {
		if c.mode == "quota_failure" {
			c.failed = true
			return nil, errors.New("private quota write failure")
		}
		return driver.RowsAffected(1), nil
	}
	if strings.HasPrefix(q, `UPDATE "mailboxes"`) {
		if c.mode == "full" {
			return driver.RowsAffected(0), nil
		}
		for _, arg := range args {
			if n, ok := arg.Value.(int64); ok {
				c.state.quota += n
				break
			}
		}
		return driver.RowsAffected(1), nil
	}
	return nil, fmt.Errorf("unexpected import write: %s", q)
}

type importFixture struct {
	c      *importConnection
	router http.Handler
	token  string
	key    string
}

func newImportFixture(t *testing.T, mode string) *importFixture {
	t.Helper()
	c := &importConnection{mode: mode}
	key, public, hash, keyErr := auth.NewAPIKey()
	if keyErr != nil {
		t.Fatal(keyErr)
	}
	c.keyPublic, c.keyHash = public, hash
	pool := sql.OpenDB(c)
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = pool.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}), &gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	jwt := auth.NewJWTService(routerTestSecret, 15*time.Minute, 7*24*time.Hour)
	session, err := jwt.GenerateSession(1, "admin@example.test", 0, []string{auth.AMRPassword}, mode == "setup")
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Environment: "test", SMTPMaxMessageSize: 1024, APIMaxBodyBytes: 70000}
	if mode == "body_limit" {
		cfg.APIMaxBodyBytes = 8
	}
	routers := NewRouters(db, jwt, cfg, nil)
	t.Cleanup(routers.Close)
	return &importFixture{c, routers.Public, session.AccessToken, key}
}

func (f *importFixture) request(t *testing.T, body any, account, token string) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/accounts/"+account+"/messages/import", bytes.NewReader(raw))
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, r)
	return w
}

func importExampleBody() map[string]any {
	return map[string]any{"raw_message": []byte("Message-ID: <history@example.test>\r\nFrom: Original <old@example.test>\r\nSubject: original\r\n\r\nhistory\r\n"), "folder": "Archive", "flags": []string{`\Seen`, `\Answered`, `\Flagged`, `\Draft`}, "internal_date": "2020-01-02T03:04:05Z"}
}

func TestMessageImportRealRoutePreservationIdempotencyAndRollback(t *testing.T) {
	useRealGrants(t)
	t.Run("preserve and skip", func(t *testing.T) {
		f := newImportFixture(t, "")
		body := importExampleBody()
		first := f.request(t, body, "12", f.token)
		if first.Code != 200 {
			t.Fatalf("first import: %d %s; queries=%v", first.Code, first.Body, f.c.queries)
		}
		if len(f.c.state.messages) != 1 {
			t.Fatalf("stored messages=%d", len(f.c.state.messages))
		}
		stored := f.c.state.messages[0]
		if stored["raw_message"] != string(body["raw_message"].([]byte)) || stored["folder"] != "Archive" || stored["is_answered"] != true || stored["is_read"] != true || stored["is_flagged"] != true || stored["is_draft"] != true {
			t.Fatalf("historical message changed: %+v", stored)
		}
		if got, ok := stored["received_at"].(time.Time); !ok || got.Format(time.RFC3339) != "2020-01-02T03:04:05Z" {
			t.Fatalf("internal date=%v", stored["received_at"])
		}
		beforeQuota := f.c.state.quota
		body["flags"] = []string{}
		body["internal_date"] = "2021-02-03T04:05:06Z"
		second := f.request(t, body, "12", f.token)
		if second.Code != 200 || !strings.Contains(second.Body.String(), `"imported":false`) || len(f.c.state.messages) != 1 || f.c.state.quota != beforeQuota {
			t.Fatalf("duplicate changed storage/quota: %d %s", second.Code, second.Body)
		}
		if f.c.state.messages[0]["is_answered"] != true {
			t.Fatal("duplicate overwrote flags")
		}
		body["folder"] = "Other"
		third := f.request(t, body, "12", f.token)
		if third.Code != 200 || !strings.Contains(third.Body.String(), `"imported":true`) || len(f.c.state.messages) != 2 {
			t.Fatalf("folder identity not independent: %d %s", third.Code, third.Body)
		}
	})
	for _, mode := range []string{"read_failure", "insert_failure", "quota_failure", "audit_failure", "full"} {
		t.Run(mode, func(t *testing.T) {
			f := newImportFixture(t, mode)
			w := f.request(t, importExampleBody(), "12", f.token)
			want := 500
			if mode == "full" {
				want = 409
			}
			if w.Code != want || strings.Contains(w.Body.String(), "private") || len(f.c.state.messages) != 0 || f.c.state.quota != 0 {
				t.Fatalf("failure was not atomic/sanitized: code=%d body=%s state=%+v", w.Code, w.Body, f.c.state)
			}
			if mode != "full" && !f.c.failed {
				t.Fatal("requested fault was not reached")
			}
		})
	}
}

func TestMessageImportBinarySourceAndMissingIDAreStable(t *testing.T) {
	useRealGrants(t)
	f := newImportFixture(t, "")
	body := importExampleBody()
	raw := []byte("From: original@example.test\r\nSubject: archive\r\nAuthentication-Results: old.example; spf=pass\r\n\r\nhistory\xff\x00\r\n")
	body["raw_message"] = raw
	for i := 0; i < 2; i++ {
		w := f.request(t, body, "12", f.token)
		if w.Code != 200 || !strings.Contains(w.Body.String(), fmt.Sprintf(`"imported":%t`, i == 0)) {
			t.Fatalf("binary import %d: %d %s", i, w.Code, w.Body)
		}
	}
	if len(f.c.state.messages) != 1 {
		t.Fatal("missing Message-ID duplicated identical source")
	}
	stored := f.c.state.messages[0]
	octets, ok := stored["raw_octets"].([]byte)
	if !ok || !bytes.Equal(octets, raw) || stored["raw_message"] != "" || !strings.HasPrefix(stored["message_id"].(string), "<import.") {
		t.Fatalf("binary source changed: %+v", stored)
	}
	body["raw_message"] = append(append([]byte(nil), raw...), []byte("different")...)
	if w := f.request(t, body, "12", f.token); w.Code != 200 || len(f.c.state.messages) != 2 {
		t.Fatalf("distinct missing-ID source collided: %d %s", w.Code, w.Body)
	}
}

func TestMessageImportRealAuthorizationAndRefusals(t *testing.T) {
	useRealGrants(t)
	for _, tc := range []struct {
		name, mode, account string
		status              int
	}{
		{"anonymous", "", "12", 401}, {"revoked grants", "denied", "12", 403},
		{"unknown account", "missing", "12", 403}, {"invalid account", "", "0", 403},
		{"unavailable scope", "scope_failure", "12", 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newImportFixture(t, tc.mode)
			token := f.token
			if tc.name == "anonymous" {
				token = ""
			}
			w := f.request(t, importExampleBody(), tc.account, token)
			if w.Code != tc.status || len(f.c.state.messages) != 0 {
				t.Fatalf("authorization refusal: %d %s", w.Code, w.Body)
			}
		})
	}
	for _, tc := range []struct {
		name   string
		change func(map[string]any)
		status int
	}{
		{"unsupported flag", func(b map[string]any) { b["flags"] = []string{"$Custom"} }, 422},
		{"missing date", func(b map[string]any) { delete(b, "internal_date") }, 422},
		{"bad folder", func(b map[string]any) { b["folder"] = "bad\x00folder" }, 422},
		{"decoded limit", func(b map[string]any) { b["raw_message"] = bytes.Repeat([]byte("x"), 1025) }, 413},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newImportFixture(t, "")
			body := importExampleBody()
			tc.change(body)
			w := f.request(t, body, "12", f.token)
			if w.Code != tc.status || len(f.c.state.messages) != 0 {
				t.Fatalf("body refusal: %d %s", w.Code, w.Body)
			}
		})
	}
}

func TestMessageImportRealAPIKeyCeilingAndCurrentHolder(t *testing.T) {
	useRealGrants(t)
	for _, tc := range []struct {
		mode   string
		status int
	}{{"", 200}, {"key_ceiling", 403}, {"denied", 403}} {
		t.Run(tc.mode, func(t *testing.T) {
			f := newImportFixture(t, tc.mode)
			w := f.request(t, importExampleBody(), "12", f.key)
			if w.Code != tc.status {
				t.Fatalf("API key request: %d %s", w.Code, w.Body)
			}
			if tc.status != 200 && len(f.c.state.messages) != 0 {
				t.Fatal("refused machine key imported a message")
			}
		})
	}
}

func TestMessageImportBinarySourceDownloadsUnchanged(t *testing.T) {
	useRealGrants(t)
	f := newImportFixture(t, "read_source")
	body := importExampleBody()
	raw := []byte("From: original@example.test\r\nSubject: original\r\nAuthentication-Results: old.example; spf=pass\r\n\r\nhistory\xff\x00\r\n")
	body["raw_message"] = raw
	if w := f.request(t, body, "12", f.token); w.Code != 200 {
		t.Fatalf("import: %d %s", w.Code, w.Body)
	}
	r := httptest.NewRequest(http.MethodGet, "/api/v1/messages/41/raw", nil)
	r.Header.Set("Authorization", "Bearer "+f.token)
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, r)
	if w.Code != 200 || w.Header().Get("Content-Type") != "message/rfc822" || !bytes.Equal(w.Body.Bytes(), raw) {
		t.Fatalf("raw download changed: status=%d type=%s bytes=%x", w.Code, w.Header().Get("Content-Type"), w.Body.Bytes())
	}
}
