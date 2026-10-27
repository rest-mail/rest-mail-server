package api

import (
	"bytes"
	"database/sql/driver"
	"encoding/json"
	"github.com/restmail/restmail/internal/examplepairs"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/restmail/restmail"
)

const testEmailCanonicalID = "<0a1b2c3d-4e5f-6a7b-8c9d-0e1f2a3b4c5d@restmail.test>"

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
		_, _ = w.Write(rr.Body.Bytes())
	})
	return s
}

func TestTestEmailSendExamples(t *testing.T) {
	f := newTestEmailSendScenarios(t)
	pairs, err := examplepairs.Read()
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
