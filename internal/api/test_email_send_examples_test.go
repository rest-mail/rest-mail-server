package api

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/restmail/restmail"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/grants"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

const testEmailSendPath = "/api/v1/admin/test/send"
const testEmailCanonicalID = "<0a1b2c3d-4e5f-6a7b-8c9d-0e1f2a3b4c5d@restmail.test>"

type testEmailSendConnection struct {
	testDataMutationConnection
	writes           int
	cancelAfterInbox context.CancelFunc
}

func (c *testEmailSendConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }
func (c *testEmailSendConnection) Driver() driver.Driver                        { return c }
func (c *testEmailSendConnection) Open(string) (driver.Conn, error)             { return c, nil }
func (c *testEmailSendConnection) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.HasPrefix(q, `SELECT * FROM "mailboxes"`) && strings.Contains(q, "address =") {
		c.relevant(ctx, q, args)
		if len(args) < 1 {
			return nil, errors.New("sender lookup lost address")
		}
		address, ok := args[0].Value.(string)
		if !ok {
			return nil, errors.New("mailbox lookup address type changed")
		}
		sender := address == "postmaster@restmail.test"
		if (sender && c.mode == "sender_lookup_failure") || (!sender && c.mode == "recipient_lookup_failure") {
			return nil, errors.New("private mailbox lookup detail")
		}
		rows := &authReadSnapshotRows{columns: []string{"id", "address", "display_name"}}
		if (sender && c.mode == "sender_missing") || (!sender && c.mode == "recipient_missing") {
			return rows, nil
		}
		id := int64(42)
		if sender {
			id = 41
		}
		rows.rows = [][]driver.Value{{id, address, "Fixture Postmaster"}}
		return rows, nil
	}
	if strings.HasPrefix(q, `INSERT INTO "messages"`) {
		c.relevant(ctx, q, args)
		c.writes++
		if (c.writes == 1 && c.mode == "inbox_failure") || (c.writes == 2 && c.mode == "sent_failure") {
			return nil, errors.New("private message insertion detail")
		}
		if c.writes == 1 && c.cancelAfterInbox != nil {
			c.cancelAfterInbox()
			c.cancelAfterInbox = nil
		}
		return &authReadSnapshotRows{columns: []string{"id"}, rows: [][]driver.Value{{int64(c.writes)}}}, nil
	}
	return c.accountCredentialListConnection.QueryContext(ctx, q, args)
}
func newTestEmailSendScenarios(t *testing.T) map[string]bulkQueueExampleScenario {
	t.Helper()
	unavailable, _, _, jwt := authReadBatchRouters(t)
	token := mailboxToken(t, jwt)
	previousSessionCheck := sessionCheck
	sessionCheck = cachedSessionCheck
	defer func() { sessionCheck = previousSessionCheck }()
	keys := map[string]accountListKey{}
	tokens := map[string]string{}
	for name, p := range map[string]auth.ScopedPermission{
		"key_admin":        {Capability: grants.Wildcard, Scope: "*"},
		"key_domain_read":  {Capability: middleware.CapDomainsRead, Scope: "*"},
		"key_domain":       {Capability: grants.Wildcard, Scope: "domain:example.com"},
		"key_mailbox":      {Capability: grants.Wildcard, Scope: "mailbox:owner@example.com"},
		"key_wrong_domain": {Capability: grants.Wildcard, Scope: "domain:other.test"},
	} {
		value, public, hash, err := auth.NewAPIKey()
		if err != nil {
			t.Fatal(err)
		}
		scope, err := json.Marshal(models.CredentialScope{Permissions: []auth.ScopedPermission{p}})
		if err != nil {
			t.Fatal(err)
		}
		keys[public] = accountListKey{accountReadKey{hash: hash, scope: scope}, models.CredentialKindAPIKey}
		tokens[name] = value
	}
	previous := heldGrants
	defer func() { heldGrants = previous }()
	makeRouter := func(entries []grants.Entry, fail, production bool) bulkQueueExampleScenario {
		heldGrants = func(*grants.Cache) middleware.HeldGrants {
			return func(uint) (grants.Held, error) {
				if fail {
					return grants.Held{}, errors.New("private snapshot grant detail")
				}
				return grants.HeldOf(entries...), nil
			}
		}
		store := &bulkQueueExampleStore{}
		c := &testEmailSendConnection{testDataMutationConnection: testDataMutationConnection{domainMutationConnection: domainMutationConnection{accountCredentialListConnection: accountCredentialListConnection{mode: "normal", keys: keys}, store: store}}}
		pool := sql.OpenDB(c)
		pool.SetMaxOpenConns(1)
		t.Cleanup(func() { pool.Close() })
		db := openAccountReadDB(t, pool)
		db.NowFunc = func() time.Time { return pipelineMutationTime }

		environment := "test"
		if production {
			environment = "production"
		}
		r := NewRouters(db, jwt, &config.Config{Environment: environment, APIMaxBodyBytes: 512}, nil)
		t.Cleanup(r.Close)
		return bulkQueueExampleScenario{accountReadScenario{http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			c.mode = req.Header.Get("X-Fixture-Data-Mode")
			c.nextID = 100
			c.writes = 0
			c.cancelAfterInbox = nil
			c.tx = nil
			c.aborted = false
			c.cleared = false
			ctx, cancel := context.WithCancel(context.WithValue(req.Context(), testDataMutationContextKey{}, true))
			defer cancel()
			c.cancel = nil
			if c.mode == "cancel_after_inbox" {
				c.cancelAfterInbox = cancel
			}
			if c.mode == "cancelled" {
				c.cancel = cancel
			}
			r.Public.ServeHTTP(w, req.WithContext(ctx))
		}), token}, store}
	}
	wild := []grants.Entry{{Capability: grants.Wildcard, Scope: grants.All}}
	normal := makeRouter(wild, false, false)
	out := map[string]bulkQueueExampleScenario{}
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range []string{"sendTestEmail"} {
		for name := range pairs[op] {
			out[name] = normal
		}
	}
	out["admin"] = makeRouter([]grants.Entry{{Capability: grants.Wildcard, Scope: grants.All}}, false, false)
	out["non_admin"] = makeRouter([]grants.Entry{{Capability: middleware.CapDomainsRead, Scope: grants.All}}, false, false)
	out["domain_scope"] = makeRouter([]grants.Entry{{Capability: grants.Wildcard, Scope: grants.Domain("example.com")}}, false, false)
	out["mailbox_scope"] = makeRouter([]grants.Entry{{Capability: grants.Wildcard, Scope: grants.Mailbox("owner@example.com")}}, false, false)
	out["permission_store_unavailable"] = makeRouter(nil, true, false)
	out["unauthenticated"] = bulkQueueExampleScenario{accountReadScenario{unavailable, ""}, nil}
	out["session_unavailable"] = bulkQueueExampleScenario{accountReadScenario{unavailable, token}, nil}
	for name, key := range tokens {
		s := normal
		s.token = key
		out[name] = s
	}
	missing := out["non_admin"]
	missing.token = tokens["key_admin"]
	out["key_holder_missing"] = missing
	for _, name := range []string{"cookie_session", "csrf_missing", "csrf_mismatch"} {
		name := name
		out[name] = bulkQueueExampleScenario{accountReadScenario{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r = r.Clone(r.Context())
			r.Header.Del("Authorization")
			r.AddCookie(&http.Cookie{Name: auth.AccessCookieName, Value: token})
			r.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: "domain-csrf"})
			if name == "cookie_session" {
				r.Header.Set(auth.CSRFHeaderName, "domain-csrf")
			}
			if name == "csrf_mismatch" {
				r.Header.Set(auth.CSRFHeaderName, "wrong")
			}
			normal.handler.ServeHTTP(w, r)
		}), ""}, normal.store}
	}
	out["wrong_domain"] = makeRouter([]grants.Entry{{Capability: grants.Wildcard, Scope: grants.Domain("other.test")}}, false, false)

	for _, name := range []string{"sender_missing", "recipient_missing", "sender_lookup_failure", "recipient_lookup_failure", "inbox_failure", "sent_failure", "commit_failure", "cancelled", "cancel_after_inbox"} {
		name := name
		out[name] = bulkQueueExampleScenario{accountReadScenario{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r = r.Clone(r.Context())
			r.Header.Set("X-Fixture-Data-Mode", name)
			normal.handler.ServeHTTP(w, r)
		}), token}, normal.store}
	}
	out["production"] = makeRouter(wild, false, true)
	return out
}

func testEmailSendNormalized(t *testing.T, s bulkQueueExampleScenario) bulkQueueExampleScenario {
	t.Helper()
	rawHandler := s.handler
	s.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		before := 0
		if s.store != nil {
			before = len(s.store.snapshot())
		}
		var body []byte
		var err error
		if r.Body != nil {
			body, err = io.ReadAll(io.LimitReader(r.Body, 1025))
			if err != nil {
				t.Error(err)
			}
			r.Body.Close()
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		var request struct{ From, To, Subject, Body string }
		_ = json.Unmarshal(body, &request)
		rr := httptest.NewRecorder()
		rawHandler.ServeHTTP(rr, r)
		end := time.Now()
		if rr.Code == 201 {
			var envelope map[string]json.RawMessage
			if json.Unmarshal(rr.Body.Bytes(), &envelope) != nil {
				t.Fatal("invalid test-send envelope")
			}
			var data struct {
				MessageID         uint   `json:"message_id"`
				MsgID             string `json:"msg_id"`
				From, To, Subject string
			}
			if json.Unmarshal(envelope["data"], &data) != nil {
				t.Fatal("invalid test-send data")
			}
			domain := ""
			if i := strings.LastIndex(request.From, "@"); i >= 0 {
				domain = request.From[i+1:]
			}
			if !regexp.MustCompile(`^<[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}@` + regexp.QuoteMeta(domain) + `>$`).MatchString(data.MsgID) {
				t.Fatal("generated test Message-ID lost syntax or sender domain")
			}
			if data.MessageID != 1 || data.From != request.From || data.To != request.To {
				t.Fatal("test-send response lost INBOX ID or addresses")
			}
			subject := request.Subject
			if subject == "" {
				subject = "Test email from REST Mail"
			}
			if data.Subject != subject {
				t.Error("default subject or supplied subject changed")
			}
			calls := s.store.snapshot()[before:]
			var writes []map[string]driver.Value
			begins, commits := 0, 0
			for _, call := range calls {
				switch call.query {
				case "BEGIN":
					begins++
				case "COMMIT":
					commits++
				case "DATA_CONTEXT":
					if call.args[0].Value != true {
						t.Error("test send lost HTTP context")
					}
				}
				if strings.HasPrefix(call.query, `INSERT INTO "messages"`) {
					fields, err := exampleInsertFields(call.query, call.args)
					if err != nil {
						t.Fatal(err)
					}
					writes = append(writes, fields)
				}
			}
			if begins != 1 || commits != 1 {
				t.Errorf("two test copies were not one transaction: begins%d commits%d", begins, commits)
			}
			if len(writes) != 2 {
				t.Fatalf("message writes%d want2", len(writes))
			}
			for i, fields := range writes {
				id, folder, read := int64(42), "INBOX", false
				if i == 1 {
					id, folder, read = 41, "Sent", true
				}
				if request.From == request.To {
					id = 41
				}
				if _, explicitID := fields["id"]; explicitID {
					t.Error("test copy reused an explicit database row ID")
				}
				for col, want := range map[string]driver.Value{"mailbox_id": id, "folder": folder, "is_read": read, "message_id": data.MsgID, "thread_id": data.MsgID, "sender": request.From, "sender_name": "Fixture Postmaster", "subject": subject} {
					if fields[col] != want {
						t.Errorf("copy%d lost %s: got%v want%v", i, col, fields[col], want)
					}
				}
				storedBody, ok := fields["body_text"].(string)
				if !ok {
					t.Fatal("message body lost")
				}
				if request.Body != "" {
					if storedBody != request.Body {
						t.Error("supplied message body changed")
					}
				} else {
					stamp, err := time.Parse(time.RFC3339, strings.TrimPrefix(storedBody, "This is a test email sent at "))
					if err != nil || stamp.Before(start.Truncate(time.Second)) || stamp.After(end) {
						t.Error("default body lost actual request timestamp")
					}
				}
				if fields["size_bytes"] != int64(len(storedBody)) {
					t.Error("test message size is not its UTF-8 byte count")
				}
				received, ok := fields["received_at"].(time.Time)
				if !ok || received.Before(start) || received.After(end) {
					t.Error("test message lost request-time receipt timestamp")
				}
				var raw []byte
				switch v := fields["recipients_to"].(type) {
				case string:
					raw = []byte(v)
				case []byte:
					raw = v
				default:
					t.Fatal("recipient JSON lost")
				}
				want, _ := json.Marshal([]string{request.To})
				assertPublicExampleBody(t, want, raw)
			}
			// Only the validated random identifier is normalized. Every other response
			// field remains byte/JSON exact, and both inserted IDs already match it.
			var fields map[string]json.RawMessage
			_ = json.Unmarshal(envelope["data"], &fields)
			fields["msg_id"], _ = json.Marshal(testEmailCanonicalID)
			envelope["data"], _ = json.Marshal(fields)
			normalized, _ := json.Marshal(envelope)
			rr.Body.Reset()
			rr.Body.Write(normalized)
		}
		for k, values := range rr.Header() {
			w.Header()[k] = values
		}
		w.WriteHeader(rr.Code)
		w.Write(rr.Body.Bytes())
	})
	return s
}
func TestTestEmailSendExamples(t *testing.T) {
	f := newTestEmailSendScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for name, pair := range pairs["sendTestEmail"] {
		t.Run(name, func(t *testing.T) {
			s, ok := f[name]
			if !ok {
				t.Fatal("missing test-send scenario")
			}
			s = testEmailSendNormalized(t, s)
			before := 0
			if s.store != nil {
				before = len(s.store.snapshot())
			}
			body := []byte(pair.Request.Body)
			if name == "malformed_json" {
				body = []byte("{")
			}
			if name == "request_too_large" {
				body = bytes.Repeat([]byte("x"), 513)
			}
			rr := mailboxSessionEndingRequest(t, s, http.MethodPost, testEmailSendPath, body)
			if rr.Code != pair.Response.Status {
				t.Fatalf("status%d want%d", rr.Code, pair.Response.Status)
			}
			assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
			var calls []bulkQueueExampleMutation
			if s.store != nil {
				calls = s.store.snapshot()[before:]
			}
			begins, commits, rollbacks, lookups, writes := 0, 0, 0, 0, 0
			for _, call := range calls {
				switch call.query {
				case "BEGIN":
					begins++
				case "COMMIT":
					commits++
				case "ROLLBACK":
					rollbacks++
				case "DATA_CONTEXT":
					if call.args[0].Value != true {
						t.Error("test-send work lost HTTP context")
					}
				}
				if strings.HasPrefix(call.query, `SELECT * FROM "mailboxes"`) {
					lookups++
				}
				if strings.HasPrefix(call.query, `INSERT INTO "messages"`) {
					writes++
				}
			}
			if rr.Code == 201 {
				if lookups != 2 || writes != 2 {
					t.Fatal("test send did not select both mailboxes and insert both copies")
				}
			} else if rr.Code == 404 || rr.Code == 500 {
				if begins != 1 || commits != 0 || (name != "commit_failure" && rollbacks != 1) {
					t.Fatal("failed test send escaped rollback")
				}
				expectedWrites := 0
				switch name {
				case "inbox_failure", "cancel_after_inbox":
					expectedWrites = 1
				case "sent_failure", "commit_failure":
					expectedWrites = 2
				}
				if writes != expectedWrites {
					t.Fatalf("failed send wrote%d rows want%d", writes, expectedWrites)
				}
			} else if len(calls) != 0 {
				t.Fatal("refused test send accessed message resources")
			}
		})
	}
	for _, g := range gapGenerators(t) {
		for _, w := range g.Report() {
			if w.OperationID == "sendTestEmail" && gapWarningApplies(map[string]gapOperation{"sendTestEmail": {}}, w) {
				t.Errorf("send warning:%v", w)
			}
		}
	}
}
func TestTestEmailSendExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	f := newTestEmailSendScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for name, pair := range pairs["sendTestEmail"] {
		t.Run(name, func(t *testing.T) {
			checkQueueMutationPairWithVertrag(t, binary, testEmailSendNormalized(t, f[name]), http.MethodPost, testEmailSendPath, name, pair, 513)
		})
	}
}
func TestTestEmailSendProductionAbsent(t *testing.T) {
	s := newTestEmailSendScenarios(t)["production"]
	rr := mailboxSessionEndingRequest(t, s, http.MethodPost, testEmailSendPath, []byte(`{}`))
	if rr.Code != 404 || len(s.store.snapshot()) != 0 {
		t.Fatal("production exposed test injection")
	}
}
