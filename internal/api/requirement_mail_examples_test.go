package api

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/api/handlers"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/examplepairs"
)

type mailRequirementConnection struct {
	*authRequirementConnection
	readOnly, released, removed bool
	policyReads                 int
	policyRejections            int
	messages, queue             []map[string]any
	scheduled                   map[string]any
	quota                       int64
}

func (c *mailRequirementConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }
func (c *mailRequirementConnection) Driver() driver.Driver                        { return c }
func (c *mailRequirementConnection) Open(string) (driver.Conn, error)             { return c, nil }
func (c *mailRequirementConnection) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if rows, ok := defaultServerCeilingRows(q, args); ok {
		return rows, nil
	}
	switch {
	case strings.Contains(q, "JOIN role_capabilities"):
		cols := []string{"scope", "capability", "expires_at", "activation_expires_at"}
		if c.readOnly {
			return invitationRows(cols, []driver.Value{"mailbox:admin@restmail.test", "mail:read", nil, nil}), nil
		}
		if c.op == "deliverMessage" {
			// The fixed recipient holds its default receive grant.
			return invitationRows(cols, []driver.Value{"mailbox:admin@restmail.test", "mail:receive", nil, nil}), nil
		}
		return invitationRows(cols), nil
	case strings.Contains(q, `FROM "mailboxes"`):
		if strings.Contains(q, "owner_id") {
			address := "admin@restmail.test"
			if c.readOnly {
				address = "actor@other.test"
			}
			return invitationRows([]string{"address", "owner_id"}, []driver.Value{address, nil}), nil
		}
		for _, arg := range args {
			if value, ok := arg.Value.(string); ok && strings.Contains(value, "@") && value != "admin@restmail.test" && value != "eve@restmail.test" {
				return invitationRows([]string{"id"}), nil
			}
		}
		id, address := int64(1), "admin@restmail.test"
		if c.op == "linkAccount" {
			id, address = 2, "eve@restmail.test"
		} else if len(args) > 0 {
			if requested, ok := args[0].Value.(int64); ok {
				id = requested
				switch id {
				case 2:
					address = "eve@restmail.test"
				case 10:
					address = "actor@other.test"
				case 42:
					address = "other@restmail.test"
				}
			}
		}
		return importRows(map[string]any{"id": id, "domain_id": int64(1), "address": address, "active": true, "display_name": "Admin", "quota_bytes": int64(1 << 30)}), nil
	case strings.Contains(q, `FROM "domains"`):
		if c.name == "rejected" && strings.HasPrefix(q, `SELECT "id"`) {
			return invitationRows([]string{"id"}, []driver.Value{int64(1)}), nil
		}
		if strings.Contains(q, "sending_policy") {
			return importRows(map[string]any{"id": int64(1), "name": "restmail.test", "sending_policy": "transactional"}), nil
		}
		return invitationRows([]string{"id"}), nil // No signing key; remote organizer/sender.
	case strings.Contains(q, `FROM "messages"`):
		id, mailbox := int64(1), int64(1)
		if c.name == "foreign_owner" {
			id, mailbox = 12, 42
		}
		return importRows(map[string]any{"id": id, "mailbox_id": mailbox, "folder": "Drafts", "is_draft": true, "sender": "organizer@remote.test", "subject": "Planning", "body_text": "Hello", "recipients_to": []byte(`["recipient@example.net"]`), "calendar_events": []byte(`[{"uid":"planning@remote.test","summary":"Planning","organizer":{"address":"organizer@remote.test"}}]`)}), nil
	case strings.Contains(q, `FROM "attachments"`), strings.Contains(q, `FROM "sieve_scripts"`):
		return invitationRows([]string{"id"}), nil
	case strings.Contains(q, `FROM "pipelines"`):
		c.policyReads++
		step := map[string]any{"name": "sieve", "type": "action", "enabled": true, "config": map[string]string{"script": "discard;"}}
		if c.name == "rejected" {
			step["name"] = "domain_allowlist"
			step["config"] = map[string]string{"reject_message": "Message rejected by policy"}
		}
		filter, err := json.Marshal([]any{step})
		if err != nil {
			return nil, err
		}
		return importRows(map[string]any{"id": int64(5), "domain_id": int64(1), "active": true, "direction": "inbound", "filters": filter}), nil
	case strings.Contains(q, `FROM "domain_sender_rules"`):
		if c.name != "rejected" || !strings.Contains(q, "list_type = 'block'") {
			return nil, fmt.Errorf("unexpected sender rule lookup: %s", q)
		}
		c.policyRejections++
		return invitationRows([]string{"count"}, []driver.Value{int64(1)}), nil
	case strings.Contains(q, `FROM "quarantine"`):
		return importRows(map[string]any{"id": int64(41), "mailbox_id": int64(1), "sender": "sender@example.net", "subject": "Hello", "raw_message": "Subject: Hello\r\n\r\nHello", "released": c.released}), nil
	case strings.HasPrefix(q, `INSERT INTO "messages"`):
		f, err := invitationFields(q, args)
		if err != nil {
			return nil, err
		}
		c.messages = append(c.messages, f)
		return invitationRows([]string{"id"}, []driver.Value{int64(41)}), nil
	case strings.HasPrefix(q, `INSERT INTO "outbound_queue"`):
		f, err := invitationFields(q, args)
		if err != nil {
			return nil, err
		}
		c.queue = append(c.queue, f)
		return invitationRows([]string{"id"}, []driver.Value{int64(51)}), nil
	case strings.HasPrefix(q, `INSERT INTO "scheduled_sends"`):
		f, err := invitationFields(q, args)
		if err != nil {
			return nil, err
		}
		c.scheduled = f
		return invitationRows([]string{"id"}, []driver.Value{int64(51)}), nil
	case strings.HasPrefix(q, `INSERT INTO "message_traces"`):
		return invitationRows([]string{"id"}, []driver.Value{int64(61)}), nil
	case strings.HasPrefix(q, "SELECT count(*)") && strings.Contains(q, `FROM "roles"`):
		return invitationRows([]string{"count"}, []driver.Value{int64(0)}), nil // The linked account receives its owner role.
	case strings.Contains(q, `FROM "roles"`):
		return invitationRows([]string{"id", "name"}, []driver.Value{int64(3), "owner"}), nil
	case strings.HasPrefix(q, `INSERT INTO "grants"`):
		return invitationRows([]string{"id"}), nil // Link already exists: ON CONFLICT DO NOTHING.
	case strings.Contains(q, `FROM "grants"`):
		return importRows(map[string]any{"id": int64(7), "holder_kind": "account", "holder_id": int64(1), "role_id": int64(3), "scope": "mailbox:eve@restmail.test", "mode": "active"}), nil
	case strings.Contains(q, `FROM "grant_activations"`):
		return invitationRows([]string{"id"}), nil
	case strings.Contains(q, "FROM grants AS g") && strings.Contains(q, "count(*)"):
		return invitationRows([]string{"count"}, []driver.Value{int64(1)}), nil
	}
	return c.authRequirementConnection.QueryContext(ctx, q, args)
}
func (c *mailRequirementConnection) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	switch {
	case strings.HasPrefix(q, `UPDATE "quarantine"`):
		c.released = true
		return driver.RowsAffected(1), nil
	case strings.HasPrefix(q, `UPDATE "mailboxes"`) && strings.Contains(q, "quota_used_bytes"):
		for _, arg := range args {
			if n, ok := arg.Value.(int64); ok {
				c.quota = n
				break
			}
		}
		return driver.RowsAffected(1), nil
	case strings.HasPrefix(q, `DELETE FROM "grant_activations"`):
		return driver.RowsAffected(0), nil
	case strings.HasPrefix(q, `DELETE FROM "grants"`):
		c.removed = true
		return driver.RowsAffected(1), nil
	}
	return c.authRequirementConnection.ExecContext(ctx, q, args)
}

func TestMailStateRequirementExamples(t *testing.T) {
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	routes := requirementRoutes(t)
	for _, tc := range []struct{ op, name string }{
		{"deliverMessage", "processed"}, {"deliverMessage", "rejected"}, {"sendMessage", "discarded"}, {"forwardMessage", "discarded"}, {"sendMessage", "scheduled"},
		{"respondToCalendar", "accepted"}, {"respondToCalendar", "foreign_sender"}, {"releaseQuarantine", "released"}, {"releaseQuarantine", "read_only"},
		{"accountEvents", "inaccessible"}, {"updateMessage", "read_only"}, {"createAccount", "outside_domain"}, {"restmailDeliver", "unauthenticated_delivery"},
		{"linkAccount", "already_linked"}, {"unlinkAccount", "unlinked"},
		{"saveDraft", "foreign_sender"}, {"updateDraft", "foreign_owner"}, {"sendDraft", "foreign_owner"}, {"saveDraft", "malformed_json"}, {"updateDraft", "malformed_json"},
	} {
		t.Run(tc.op+"/"+tc.name, func(t *testing.T) {
			examplepairs.Proven(t, tc.op, tc.name)
			if tc.op == "restmailDeliver" {
				t.Setenv("RESTMAIL_DELIVER_STRICT", "true")
			}
			c := &mailRequirementConnection{authRequirementConnection: &authRequirementConnection{t: t, op: tc.op, name: tc.name, password: requirementHash(t, "supplied-by-contract-hook"), revoked: true}, readOnly: tc.name == "read_only"}
			jwt := auth.NewJWTService(routerTestSecret, time.Minute, time.Hour)
			r := NewRouters(requirementDB(t, c), jwt, &config.Config{Environment: "test", MasterKey: accessMasterKey}, nil)
			t.Cleanup(r.Close)
			actor, address := uint(1), "admin@restmail.test"
			if c.readOnly {
				actor, address = 10, "actor@other.test"
			}
			session, err := jwt.GenerateSession(actor, address, 0, []string{auth.AMRPassword}, false)
			if err != nil {
				t.Fatal(err)
			}
			pair := pairs[tc.op][tc.name]
			if tc.name == "scheduled" {
				var body map[string]any
				if err := json.Unmarshal(pair.Request.Body, &body); err != nil {
					t.Fatal(err)
				}
				body["scheduled_at"] = time.Now().UTC().Add(time.Hour).Truncate(time.Second).Format(time.RFC3339)
				pair.Request.Body, err = json.Marshal(body)
				if err != nil {
					t.Fatal(err)
				}
			}
			req := requirementRequest(routes[tc.op], pair)
			req.Header.Set("Authorization", "Bearer "+session.AccessToken)
			if tc.name == "malformed_json" {
				req = httptest.NewRequest(routes[tc.op].method, messageCollectionReadURI(routes[tc.op].path, pair), strings.NewReader("{"))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Authorization", "Bearer "+session.AccessToken)
			}
			rr := httptest.NewRecorder()
			started := time.Now()
			r.Public.ServeHTTP(rr, req)
			finished := time.Now()
			if tc.name == "scheduled" && rr.Code == 202 {
				var got, want map[string]map[string]any
				if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(pair.Response.Body, &want); err != nil {
					t.Fatal(err)
				}
				if c.scheduled == nil || got["data"]["id"] != float64(51) || got["data"]["account_id"] != float64(1) {
					t.Fatal("schedule identity not persisted")
				}
				for _, key := range []string{"scheduled_at", "created_at", "updated_at"} {
					stamp, err := time.Parse(time.RFC3339Nano, got["data"][key].(string))
					if err != nil {
						t.Fatal(err)
					}
					if key == "scheduled_at" {
						if !stamp.Equal(c.scheduled["send_at"].(time.Time)) {
							t.Fatal("scheduled time not persisted")
						}
					} else if stamp.Before(started) || stamp.After(finished) {
						t.Fatal("incorrect schedule timestamp")
					}
					got["data"][key] = want["data"][key]
				}
				var stored map[string]any
				raw := c.scheduled["request"]
				if b, ok := raw.([]byte); ok {
					err = json.Unmarshal(b, &stored)
				} else {
					err = json.Unmarshal([]byte(raw.(string)), &stored)
				}
				if err != nil || stored["scheduled_at"] != nil || stored["subject"] != "Hello" {
					t.Fatal("scheduled replay request is incorrect")
				}
				data, err := json.Marshal(got)
				if err != nil {
					t.Fatal(err)
				}
				rr.Body.Reset()
				rr.Body.Write(data)
			}
			assertRequirementResponse(t, pair, rr)
			switch tc.name {
			case "processed", "rejected", "discarded":
				if c.policyReads == 0 || len(c.messages) != 0 || len(c.queue) != 0 {
					t.Fatal("terminal pipeline result stored or queued mail, or never read policy")
				}
				if tc.name == "rejected" && c.policyRejections != 1 {
					t.Fatal("configured domain block was not checked")
				}
			case "released":
				if !c.released || len(c.messages) != 1 || c.messages[0]["folder"] != "INBOX" || c.messages[0]["raw_message"] != "Subject: Hello\r\n\r\nHello" || c.quota != int64(len("Subject: Hello\r\n\r\nHello")) {
					t.Fatal("release did not preserve content and charge quota")
				}
			case "accepted":
				if len(c.messages) != 1 || len(c.queue) != 1 || c.queue[0]["recipient"] != "organizer@remote.test" || !strings.Contains(c.queue[0]["raw_message"].(string), "PARTSTAT=ACCEPTED") {
					t.Fatal("calendar reply not persisted and queued for organizer")
				}
			case "unlinked":
				if !c.removed {
					t.Fatal("link grant not removed")
				}
			default:
				if len(c.messages) != 0 || len(c.queue) != 0 || c.released || c.removed {
					t.Fatal("refusal changed mail or link state")
				}
			}
		})
	}
}

type requirementStreamWriter struct {
	*httptest.ResponseRecorder
	onFlush func()
}

func (w requirementStreamWriter) Flush() { w.ResponseRecorder.Flush(); w.onFlush() }

func TestEventStreamRequirementExample(t *testing.T) {
	examplepairs.Proven(t, "accountEvents", "stream")
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	pair := pairs["accountEvents"]["stream"]
	c := &mailRequirementConnection{authRequirementConnection: &authRequirementConnection{t: t, op: "accountEvents", name: "stream"}}
	db := requirementDB(t, c)
	broker := handlers.NewSSEBroker()
	jwt := auth.NewJWTService(routerTestSecret, time.Minute, time.Hour)
	router := chi.NewRouter()
	router.Use(middleware.JWTMiddleware(jwt), middleware.CSRF(), middleware.SetupDone)
	handlers.NewEventHandler(db, broker, func(uint, int) (bool, error) { return true, nil }).Register(endpoint.Routes{Router: router, Registry: endpoint.NewRegistry()})
	session, err := jwt.GenerateSession(1, "admin@restmail.test", 0, []string{auth.AMRPassword}, false)
	if err != nil {
		t.Fatal(err)
	}
	route := requirementRoutes(t)["accountEvents"]
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req := requirementRequest(route, pair).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+session.AccessToken)
	rr := httptest.NewRecorder()
	flushed := false
	w := requirementStreamWriter{rr, func() {
		if !flushed {
			flushed = true
			broker.Publish(1, handlers.SSEEvent{Type: "ping", Data: map[string]any{}})
		} else {
			cancel()
		}
	}}
	router.ServeHTTP(w, req)
	if rr.Code != pair.Response.Status || rr.Header().Get("Content-Type") != pair.Response.MediaType {
		t.Fatalf("stream: %d %q", rr.Code, rr.Header().Get("Content-Type"))
	}
	var expected string
	if err := json.Unmarshal(pair.Response.Body, &expected); err != nil {
		t.Fatal(err)
	}
	if rr.Body.String() != expected {
		t.Fatalf("stream got %q, want %q", rr.Body.String(), expected)
	}
	for range handlers.DefaultMaxSSEStreamsPerAccount {
		if !broker.AcquireStream(1) {
			t.Fatal("stream slot leaked")
		}
		t.Cleanup(func() { broker.ReleaseStream(1) })
	}
}
