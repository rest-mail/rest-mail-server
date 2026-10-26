package api

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

type messageCollectionReadConnection struct{ personalDataReadConnection }

func (c messageCollectionReadConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }

func (c messageCollectionReadConnection) Driver() driver.Driver { return c }

func (c messageCollectionReadConnection) Open(string) (driver.Conn, error) { return c, nil }

// Fixed SQL snapshots prove the real query, authorization, cursor and mapper
// wiring. They do not prove PostgreSQL filtering, ordering or persistence.
func (c messageCollectionReadConnection) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if !strings.Contains(query, `FROM "messages"`) {
		return c.personalDataReadConnection.QueryContext(ctx, query, args)
	}
	if len(args) < 3 || !strings.Contains(query, "mailbox_id =") || !strings.Contains(query, "is_deleted =") || args[2].Value != false {
		return nil, errors.New("message collection lost account or deletion restriction")
	}
	id := fmt.Sprint(args[0].Value)
	target := fmt.Sprint(args[1].Value)
	thread := strings.Contains(query, "thread_id =")
	if strings.Contains(query, "count(*)") {
		if !strings.Contains(query, "folder =") {
			return nil, errors.New("message count lost folder restriction")
		}
		if id == "5" {
			return nil, errors.New("private message count detail")
		}
		total := int64(3)
		if id == "2" || target == "Unknown" {
			total = 0
		}
		return &authReadSnapshotRows{columns: []string{"count"}, rows: [][]driver.Value{{total}}}, nil
	}
	if id == "4" {
		return nil, errors.New("private message collection detail")
	}
	columns := []string{"id", "mailbox_id", "folder", "message_id", "in_reply_to", "references", "thread_id", "sender", "sender_name", "recipients_to", "recipients_cc", "subject", "body_text", "body_html", "headers", "calendar_events", "size_bytes", "raw_size", "received_tls", "tls_version", "has_attachments", "is_read", "is_dsn", "is_flagged", "is_starred", "is_draft", "is_deleted", "received_at", "date_header", "created_at", "updated_at", "raw_message", "require_tls"}
	r := &authReadSnapshotRows{columns: columns}
	if id == "2" || target == "Unknown" || target == "unknown-thread" {
		return r, nil
	}
	ids := []int64{51, 53, 52}
	folder := "INBOX"
	if thread {
		if target != "thread/2026@example.net" || !strings.Contains(query, "ORDER BY received_at ASC") {
			return nil, errors.New("thread lost identity or chronology")
		}
		ids = []int64{52, 53, 51}
	} else {
		if !strings.Contains(query, "folder =") || !strings.Contains(query, "ORDER BY received_at DESC, id DESC") || !strings.Contains(query, "LIMIT") {
			return nil, errors.New("message page lost folder, ordering or bound")
		}
		folder = target
		if strings.Contains(query, "received_at <") {
			if len(args) != 7 || !strings.Contains(query, "received_at =") || !strings.Contains(query, "id <") {
				return nil, errors.New("message cursor lost date and ID tie breaker")
			}
			date, ok := args[3].Value.(time.Time)
			if !ok || args[4].Value != date {
				return nil, errors.New("message cursor lost repeated date")
			}
			switch args[5].Value {
			case int64(51):
				if date != time.Date(2026, 10, 20, 2, 0, 0, 0, time.UTC) {
					return nil, errors.New("wrong newest cursor date")
				}
				ids = ids[1:]
			case int64(53):
				if date != time.Date(2026, 10, 20, 1, 0, 0, 0, time.UTC) {
					return nil, errors.New("wrong tie cursor date")
				}
				ids = ids[2:]
			default:
				return nil, errors.New("unexpected message cursor identity")
			}
		}
		limit, ok := args[len(args)-1].Value.(int64)
		if !ok || (limit != 2 && limit != 51 && limit != 101) {
			return nil, errors.New("message list lost limit plus lookahead")
		}
		if int64(len(ids)) > limit {
			ids = ids[:limit]
		}
	}
	mailbox, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return nil, err
	}
	stamp := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	for _, messageID := range ids {
		received := stamp.Add(time.Hour)
		if messageID == 51 {
			received = stamp.Add(2 * time.Hour)
		}
		var body, html, tlsVersion string
		var headers, events, to, cc, encrypted, date driver.Value
		var attachments, read, dsn, flagged, starred bool
		if messageID == 51 {
			body = "Hello café"
			html = "<p>Hello café</p>"
			headers = []byte(`{"Subject":["Hello café"],"X-Trace":["a","b"]}`)
			events = []byte(`[{"uid":"event-1"}]`)
			to = []byte(`["recipient@example.net"]`)
			cc = []byte(`["copy@example.net"]`)
			encrypted = true
			tlsVersion = "TLS1.3"
			date = stamp
			attachments = true
			flagged = true
			starred = true
		}
		if messageID == 52 {
			body = "Plain delivery"
			encrypted = false
			read = true
			dsn = true
		}
		r.rows = append(r.rows, []driver.Value{messageID, mailbox, folder, fmt.Sprintf("<message-%d@example.net>", messageID), "<parent@example.net>", "<parent@example.net>", "thread/2026@example.net", "sender@example.net", "Sender café", to, cc, "Subject café", body, html, headers, events, int64(80), int64(128), encrypted, tlsVersion, attachments, read, dsn, flagged, starred, false, false, received, date, stamp, stamp, "private-raw-message-sentinel", true})
	}
	return r, nil
}

func messageCollectionReadScenarios(t *testing.T) map[string]accountReadScenario {
	t.Helper()
	out := accountListScenarios(t, func(mode string, keys map[string]accountListKey) driver.Connector {
		return messageCollectionReadConnection{personalDataReadConnection{accountCredentialListConnection{mode: mode, keys: keys}}}
	})
	for _, name := range []string{"first_page", "next_page", "last_page", "invalid_cursor", "partial_cursor", "blank_cursor", "default_limit", "max_limit", "invalid_limit", "encoded_folder", "unknown_folder", "unknown_thread", "count_unavailable"} {
		out[name] = out["found"]
	}
	out["domain_grant"] = out["domain_manager"]
	out["app_password_other"] = out["app_password"]
	return out
}

func TestMessageCountFailureIsNotAValidPage(t *testing.T) {
	s := messageCollectionReadScenarios(t)["found"]
	req := httptest.NewRequest(http.MethodGet, "/api/v1/accounts/5/folders/INBOX/messages", nil)
	req.Header.Set("Authorization", "Bearer "+s.token)
	rr := httptest.NewRecorder()
	s.handler.ServeHTTP(rr, req)
	if rr.Code != 500 {
		t.Fatalf("got %d %s, want sanitized 500", rr.Code, rr.Body.String())
	}
}

func TestMessageCollectionDoesNotInventMailboxAssociation(t *testing.T) {
	s := messageCollectionReadScenarios(t)["found"]
	for _, path := range []string{"/api/v1/accounts/1/folders/INBOX/messages", "/api/v1/accounts/1/threads/thread%2F2026@example.net"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+s.token)
		rr := httptest.NewRecorder()
		s.handler.ServeHTTP(rr, req)
		if rr.Code != 200 {
			t.Fatalf("%s: %d %s", path, rr.Code, rr.Body.String())
		}
		var response struct {
			Data []map[string]interface{} `json:"data"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if len(response.Data) != 3 {
			t.Fatalf("incomplete fixture: %s", rr.Body.String())
		}
		for _, message := range response.Data {
			if mailbox, ok := message["mailbox"]; ok {
				t.Errorf("unloaded mailbox was serialized: %v", mailbox)
			}
		}
	}
}
