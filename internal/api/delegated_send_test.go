package api

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/api/handlers"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/examplepairs"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Only persistence is substituted: route binding, live grants, credential bounds,
// sender selection, MIME rendering and audit attribution run as in production.
type delegatedSendConnection struct {
	forwardConnection
	capabilities []string
	scope        string
	represented  string // a mailbox the actor sends from, other than the fixture's sender
}

func (c *delegatedSendConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }
func (c *delegatedSendConnection) Driver() driver.Driver                        { return c }
func (c *delegatedSendConnection) Open(string) (driver.Conn, error)             { return c, nil }
func (c *delegatedSendConnection) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	if strings.HasPrefix(q, `UPDATE "messages"`) || strings.HasPrefix(q, `DELETE FROM "messages"`) {
		return driver.RowsAffected(1), nil
	}
	return c.importConnection.ExecContext(ctx, q, args)
}
func (c *delegatedSendConnection) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(q, "JOIN role_capabilities") {
		rows := [][]driver.Value{}
		for _, cap := range c.capabilities {
			rows = append(rows, []driver.Value{c.scope, cap, nil, nil})
		}
		return invitationRows([]string{"scope", "capability", "expires_at", "activation_expires_at"}, rows...), nil
	}
	if strings.Contains(q, `FROM "mailboxes"`) && c.represented != "" {
		for _, arg := range args {
			if arg.Value == c.represented || arg.Value == int64(2) {
				return importRows(map[string]any{"id": int64(2), "domain_id": int64(2), "address": c.represented, "active": true}), nil
			}
		}
	}
	if strings.Contains(q, `FROM "mailboxes"`) {
		actor := false
		for _, arg := range args {
			if arg.Value == int64(c.actorID) || arg.Value == "actor@example.test" {
				actor = true
			}
		}
		if actor {
			return importRows(map[string]any{"id": int64(c.actorID), "domain_id": int64(2), "address": "actor@example.test", "active": true}), nil
		}
	}
	return c.forwardConnection.QueryContext(ctx, q, args)
}

func TestDelegatedSendingCapabilitiesAndAudit(t *testing.T) {
	useRealGrants(t)
	for _, route := range []string{"compose", "forward", "draft"} {
		for _, tc := range []struct {
			name, cap       string
			behalf, allowed bool
		}{
			{"send_as", "mail:send_as", false, true},
			{"on_behalf", "mail:send_on_behalf", true, true},
			{"send_as_missing", "", false, false},
			{"on_behalf_missing", "", true, false},
			{"read_only_as", "mail:read", false, false},
			{"read_only_behalf", "mail:read", true, false},
			{"send_is_not_send_as", "mail:send", false, false},
			{"as_is_not_behalf", "mail:send_as", true, false},
			{"behalf_is_not_as", "mail:send_on_behalf", false, false},
			{"as_credential_denied", "mail:send_as", false, false},
			{"behalf_credential_denied", "mail:send_on_behalf", true, false},
			{"as_wrong_scope", "mail:send_as", false, false},
			{"behalf_wrong_scope", "mail:send_on_behalf", true, false},
		} {
			t.Run(route+"/"+tc.name, func(t *testing.T) {
				actorID := uint(200000 + forwardActorSequence.Add(1))
				c := &delegatedSendConnection{forwardConnection: forwardConnection{actorID: actorID, original: map[string]any{"id": int64(7), "mailbox_id": int64(1), "sender": "sender@example.test", "is_draft": true, "recipients_to": []byte(`["target@remote.test"]`), "recipients_cc": []byte(`[]`), "subject": "hello"}}, scope: "mailbox:sender@example.test"}
				if route == "forward" {
					c.capabilities = append(c.capabilities, "mail:read")
				}
				if route == "draft" {
					c.capabilities = append(c.capabilities, "mail:write")
				}
				if tc.cap != "" {
					c.capabilities = append(c.capabilities, tc.cap)
				}
				if strings.HasSuffix(tc.name, "wrong_scope") {
					c.scope = "mailbox:unrelated@example.test"
				}
				pool := sql.OpenDB(c)
				pool.SetMaxOpenConns(1)
				t.Cleanup(func() { _ = pool.Close() })
				db, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}), &gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
				if err != nil {
					t.Fatal(err)
				}
				var entries []*models.AuditEntry
				auditor := middleware.NewAuditor(func(e *models.AuditEntry) error { entries = append(entries, e); return nil }, nil, nil)
				router := chi.NewRouter()
				handlers.NewMessageHandler(db, nil, nil, "", nil).RegisterCompose(endpoint.Routes{Router: router, Registry: endpoint.NewRegistry()}, func(action string, opts ...middleware.AuditOption) endpoint.Middleware {
					return endpoint.Middleware{Wrap: auditor.Audit(action, opts...)}
				})
				path := "/api/v1/messages/send"
				body := map[string]any{"from": "sender@example.test", "to": []string{"target@remote.test"}, "subject": "hello"}
				if route == "forward" {
					path = "/api/v1/messages/7/forward"
					delete(body, "subject")
				}
				if route == "draft" {
					path = "/api/v1/messages/draft/7/send"
					body = map[string]any{}
				}
				if tc.behalf {
					body["on_behalf"] = true
				}
				encoded, _ := json.Marshal(body)
				claims := &auth.Claims{AccountID: actorID, Address: "actor@example.test", CredentialID: 91, CredentialKind: auth.CredentialKindAPIKey}
				if route == "forward" {
					claims.APIKeyPermissions = append(claims.APIKeyPermissions, auth.ScopedPermission{Scope: c.scope, Capability: "mail:read"})
				}
				if route == "draft" {
					claims.APIKeyPermissions = append(claims.APIKeyPermissions, auth.ScopedPermission{Scope: c.scope, Capability: "mail:write"})
				}
				if tc.cap != "" {
					claims.APIKeyPermissions = append(claims.APIKeyPermissions, auth.ScopedPermission{Scope: c.scope, Capability: tc.cap})
				}
				if strings.HasSuffix(tc.name, "credential_denied") {
					claims.APIKeyPermissions = claims.APIKeyPermissions[:len(claims.APIKeyPermissions)-1]
				}
				req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(encoded)))
				req = req.WithContext(context.WithValue(req.Context(), middleware.ClaimsKey, claims))
				w := httptest.NewRecorder()
				router.ServeHTTP(w, req)
				want := 403
				if tc.allowed {
					want = 201
				}
				if w.Code != want {
					t.Fatalf("got %d %s, want %d; queries=%v", w.Code, w.Body, want, c.queries)
				}
				if len(entries) != 1 || entries[0].ActorID == nil || *entries[0].ActorID != actorID || entries[0].CredentialID == nil || *entries[0].CredentialID != 91 || entries[0].ActorName != "actor@example.test" {
					t.Fatalf("actor/credential lost: %+v", entries)
				}
				if !tc.allowed {
					if len(c.queue) != 0 || len(c.state.messages) != 0 {
						t.Fatal("refused send persisted outbound work")
					}
					return
				}
				if entries[0].Target["mailbox"] != "sender@example.test" || entries[0].Target["mailbox_id"] != "1" {
					t.Fatalf("represented mailbox absent: %+v", entries[0].Target)
				}
				mode := "send_as"
				if tc.behalf {
					mode = "send_on_behalf"
				}
				if entries[0].Target["send_mode"] != mode {
					t.Fatalf("audit mode: %+v", entries[0].Target)
				}
				if len(c.queue) != 1 {
					t.Fatalf("queued %d messages", len(c.queue))
				}
				raw := c.queue[0]["raw_message"].(string)
				message, err := mail.ReadMessage(strings.NewReader(raw))
				if err != nil {
					t.Fatal(err)
				}
				from, err := mail.ParseAddress(message.Header.Get("From"))
				if err != nil || from.Address != "sender@example.test" {
					t.Fatalf("From=%q: %v", message.Header.Get("From"), err)
				}
				sender := message.Header.Get("Sender")
				if tc.behalf {
					address, err := mail.ParseAddress(sender)
					if err != nil || address.Address != "actor@example.test" {
						t.Fatalf("Sender=%q: %v", sender, err)
					}
				} else if sender != "" {
					t.Fatalf("Send As exposed Sender=%q", sender)
				}
			})
		}
	}
}

// The delegated sendMessage examples, sent as written through the compose
// route with the live grant each one requires on the represented mailbox.
func TestDelegatedSendExamples(t *testing.T) {
	useRealGrants(t)
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	for name, capability := range map[string]string{
		"send_as": "mail:send_as", "send_on_behalf": "mail:send_on_behalf",
		"read_only_send_as": "mail:read", "read_only_send_on_behalf": "mail:read",
	} {
		t.Run(name, func(t *testing.T) {
			examplepairs.Proven(t, "sendMessage", name)
			pair := pairs["sendMessage"][name]
			actorID := uint(200000 + forwardActorSequence.Add(1))
			c := &delegatedSendConnection{forwardConnection: forwardConnection{actorID: actorID}, capabilities: []string{capability}, scope: "mailbox:shared@restmail.test", represented: "shared@restmail.test"}
			pool := sql.OpenDB(c)
			pool.SetMaxOpenConns(1)
			t.Cleanup(func() { _ = pool.Close() })
			db, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}), &gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
			if err != nil {
				t.Fatal(err)
			}
			router := chi.NewRouter()
			handlers.NewMessageHandler(db, nil, nil, "", nil).RegisterCompose(endpoint.Routes{Router: router, Registry: endpoint.NewRegistry()}, func(string, ...middleware.AuditOption) endpoint.Middleware {
				return endpoint.Middleware{Wrap: func(next http.Handler) http.Handler { return next }}
			})
			claims := &auth.Claims{AccountID: actorID, Address: "actor@example.test"}
			req := httptest.NewRequest(http.MethodPost, "/api/v1/messages/send", strings.NewReader(string(pair.Request.Body)))
			req = req.WithContext(context.WithValue(req.Context(), middleware.ClaimsKey, claims))
			rr := httptest.NewRecorder()
			started := time.Now()
			router.ServeHTTP(rr, req)
			finished := time.Now()
			if rr.Code != pair.Response.Status {
				t.Fatalf("got %d %s, want %d", rr.Code, rr.Body, pair.Response.Status)
			}
			if rr.Code != http.StatusCreated {
				if len(c.queue) != 0 || len(c.state.messages) != 0 {
					t.Fatal("refused send persisted outbound work")
				}
				assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
				return
			}
			// What the server generates must be what it stored and queued; the
			// rest of the body is the example's.
			var got, want map[string]map[string]any
			if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(pair.Response.Body, &want); err != nil {
				t.Fatal(err)
			}
			if len(c.state.messages) != 1 || len(c.queue) != 1 {
				t.Fatalf("stored %d messages, queued %d", len(c.state.messages), len(c.queue))
			}
			sent, data := c.state.messages[0], got["data"]
			raw, _ := c.queue[0]["raw_message"].(string)
			if data["id"] != float64(sent["id"].(int64)) || data["message_id"] != sent["message_id"] || data["thread_id"] != sent["thread_id"] ||
				raw == "" || data["raw_size"] != float64(len(raw)) || data["downgraded_size"] != float64(len(raw)) {
				t.Fatalf("response identity differs from the stored message: %s", rr.Body)
			}
			for _, key := range []string{"id", "message_id", "thread_id", "raw_size", "downgraded_size"} {
				data[key] = want["data"][key]
			}
			for _, key := range []string{"received_at", "created_at", "updated_at"} {
				stamp, err := time.Parse(time.RFC3339Nano, data[key].(string))
				if err != nil || stamp.Before(started) || stamp.After(finished) {
					t.Fatalf("%s outside the request: %v", key, data[key])
				}
				data[key] = want["data"][key]
			}
			normalized, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			assertPublicExampleBody(t, pair.Response.Body, normalized)
		})
	}
}
