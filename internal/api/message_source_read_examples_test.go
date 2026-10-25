package api

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/restmail/restmail"
)

var messageSourceReadPaths = map[string]string{"getMessage": "/api/v1/messages/{id}", "getRawMessage": "/api/v1/messages/{id}/raw"}

const messageSourceReadRaw = "From: sender@example.net\r\nTo: recipient@example.com\r\nSubject: café\r\nX-Trace: one\r\nX-Trace: two\r\n\r\nHello café\r\n.No dot unstuffing here\r\n"

// Fixed snapshots exercise the actual message lookup, access and response
// adapters. They do not claim PostgreSQL semantics or audit persistence.
type messageSourceReadConnection struct{ personalDataReadConnection }

func (c messageSourceReadConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }
func (c messageSourceReadConnection) Driver() driver.Driver                        { return c }
func (c messageSourceReadConnection) Open(string) (driver.Conn, error)             { return c, nil }
func (c messageSourceReadConnection) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if !strings.Contains(query, `FROM "messages"`) {
		return c.personalDataReadConnection.QueryContext(ctx, query, args)
	}
	if len(args) != 2 || !strings.Contains(query, `"messages"."id" =`) || !strings.Contains(query, "LIMIT") || args[1].Value != int64(1) {
		return nil, errors.New("message source lost identity or single-row bound")
	}
	id := fmt.Sprint(args[0].Value)
	if id == "3" {
		return nil, errors.New("private message lookup detail")
	}
	r := &authReadSnapshotRows{columns: []string{"id", "mailbox_id", "folder", "message_id", "sender", "sender_name", "recipients_to", "recipients_cc", "subject", "body_text", "body_html", "headers", "calendar_events", "size_bytes", "raw_size", "received_tls", "tls_version", "has_attachments", "is_read", "is_draft", "is_deleted", "received_at", "date_header", "created_at", "updated_at", "raw_message"}}
	if id == "2147483647" {
		return r, nil
	}
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return nil, err
	}
	mailbox := n
	if id == "5" {
		mailbox = 3
	}
	if id == "6" || id == "7" {
		mailbox = 1
	}
	raw := messageSourceReadRaw
	if id == "2" {
		raw = ""
	}
	if id == "6" {
		raw = "\r\n"
	}
	stamp := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	r.rows = [][]driver.Value{{n, mailbox, "INBOX", "<message@example.net>", "sender@example.net", "Sender café", []byte(`["recipient@example.com"]`), nil, "Subject café", "Hello café", "<p>Hello café</p>", []byte(`{"X-Trace":["one","two"]}`), nil, int64(80), int64(len(raw)), true, "TLS1.3", true, false, false, id == "7", stamp, nil, stamp, stamp, raw}}
	return r, nil
}

func messageSourceReadScenarios(t *testing.T) map[string]accountReadScenario {
	t.Helper()
	out := accountListScenarios(t, func(mode string, keys map[string]accountListKey) driver.Connector {
		return messageSourceReadConnection{personalDataReadConnection{accountCredentialListConnection{mode: mode, keys: keys}}}
	})
	out["domain_grant"] = out["domain_manager"]
	out["app_password_other"] = out["app_password"]
	for _, name := range []string{"mailbox_unavailable", "source_missing", "minimal_source", "deleted_message"} {
		out[name] = out["found"]
	}
	return out
}

func TestMessageSourceLookupFailuresAreNotMissingOrDenied(t *testing.T) {
	scenarios := messageSourceReadScenarios(t)
	for _, route := range []struct{ method, path string }{{http.MethodGet, "/api/v1/messages/%s"}, {http.MethodGet, "/api/v1/messages/%s/raw"}, {http.MethodPatch, "/api/v1/messages/%s"}, {http.MethodDelete, "/api/v1/messages/%s"}} {
		for _, tc := range []struct{ id, scenario string }{{"3", "found"}, {"5", "found"}, {"1", "grant_unavailable"}, {"1", "ownership_unavailable"}} {
			s := scenarios[tc.scenario]
			req := httptest.NewRequest(route.method, fmt.Sprintf(route.path, tc.id), strings.NewReader("{}"))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+s.token)
			rr := httptest.NewRecorder()
			s.handler.ServeHTTP(rr, req)
			if rr.Code != 500 || rr.Body.String() != "{\"error\":{\"code\":\"internal_error\",\"message\":\"Internal server error\"}}\n" {
				t.Errorf("%s/%s: got %d %s, want sanitized 500", req.URL.Path, tc.scenario, rr.Code, rr.Body.String())
			}
		}
	}
}

func TestMessageSourceReadExamples(t *testing.T) {
	scenarios := messageSourceReadScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for operation, path := range messageSourceReadPaths {
		if len(pairs[operation]) < 20 {
			t.Fatalf("incomplete examples for %s", operation)
		}
		for name, pair := range pairs[operation] {
			t.Run(operation+"/"+name, func(t *testing.T) {
				s, ok := scenarios[name]
				if !ok {
					t.Fatalf("missing scenario %s", name)
				}
				req := httptest.NewRequest(http.MethodGet, messageCollectionReadURI(path, pair), nil)
				if s.token != "" {
					req.Header.Set("Authorization", "Bearer "+s.token)
				}
				if name == "request_too_large" {
					req.ContentLength = 9
				}
				rr := httptest.NewRecorder()
				s.handler.ServeHTTP(rr, req)
				if rr.Code != pair.Response.Status || rr.Header().Get("Content-Type") != pair.Response.MediaType {
					t.Fatalf("got %d %s %s, want %d %s", rr.Code, rr.Header().Get("Content-Type"), rr.Body.String(), pair.Response.Status, pair.Response.MediaType)
				}
				if name == "rate_limited" {
					retry, err := strconv.Atoi(rr.Header().Get("Retry-After"))
					if err != nil || retry < 1 {
						t.Fatal("refusal lost Retry-After")
					}
				}
				if pair.Response.MediaType == "message/rfc822" {
					var literal string
					if err := json.Unmarshal(pair.Response.Body, &literal); err != nil {
						t.Fatal(err)
					}
					if rr.Body.String() != literal {
						t.Fatalf("raw message bytes differ: got %q, want %q", rr.Body.String(), literal)
					}
				} else {
					assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
				}
			})
		}
	}
	for _, g := range gapGenerators(t) {
		for _, warning := range g.Report() {
			if _, ok := messageSourceReadPaths[warning.OperationID]; ok {
				t.Errorf("message source warning: %v", warning)
			}
		}
	}
}

func TestMessageSourceReadExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY for message source exchanges")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	scenarios := messageSourceReadScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for operation, path := range messageSourceReadPaths {
		for name, pair := range pairs[operation] {
			t.Run(operation+"/"+name, func(t *testing.T) {
				s := scenarios[name]
				checkMailboxLookupWithVertrag(t, binary, s.handler, s.token, path, name, pair)
			})
		}
	}
}
