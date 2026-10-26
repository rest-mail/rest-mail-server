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

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/db/models"
)

// The production repositories and access checks consume these fixed SQL rows.
// They prove wiring, not PostgreSQL semantics, storage access or mutation/audit persistence.
type mailResourceReadConnection struct{ personalDataReadConnection }

func (c mailResourceReadConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }

func (c mailResourceReadConnection) Driver() driver.Driver { return c }

func (c mailResourceReadConnection) Open(string) (driver.Conn, error) { return c, nil }

func (c mailResourceReadConnection) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(query, "JOIN role_capabilities") {
		rows, err := c.personalDataReadConnection.QueryContext(ctx, query, args)
		if err == nil && c.mode == "normal" {
			r := rows.(*authReadSnapshotRows)
			r.rows = append(r.rows, []driver.Value{"mailbox:linked@example.com", "mail:send", nil, nil})
		}
		return rows, err
	}
	stamp := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	if strings.Contains(query, `FROM "scheduled_sends"`) {
		if len(args) != 3 || fmt.Sprint(args[0].Value) != "10" || args[1].Value != "pending" || args[2].Value != "sending" || !strings.Contains(query, "account_id =") || !strings.Contains(query, "status IN") || !strings.Contains(query, "ORDER BY send_at") {
			return nil, errors.New("scheduled list lost owner, waiting states or ordering")
		}
		if c.mode == "list_failure" {
			return nil, errors.New("private scheduled list detail")
		}
		r := &authReadSnapshotRows{columns: []string{"id", "account_id", "from", "to", "subject", "send_at", "status", "attempts", "last_error", "created_at", "updated_at", "request"}}
		if c.mode != "primary_missing" {
			for i, address := range []string{"user@example.com", "linked@example.com", "linked@example.com", "other@example.com"} {
				recipients := driver.Value([]byte(`["recipient@example.net"]`))
				attempts := int64(0)
				lastError := ""
				status := "pending"
				if i == 1 {
					status = "sending"
					attempts = 2
					lastError = "Delivery deferred"
				}
				if i == 2 {
					recipients = nil
				}
				r.rows = append(r.rows, []driver.Value{int64(51 + i), int64(10), address, recipients, "Send café", stamp.AddDate(0, 0, 7).Add(time.Duration(i) * time.Hour), status, attempts, lastError, stamp, stamp, []byte(`{"private":"request-sentinel"}`)})
			}
		}
		return r, nil
	}
	if strings.Contains(query, `FROM "mailboxes"`) && strings.Contains(query, "address =") && !strings.Contains(query, `SELECT "address"`) {
		if len(args) == 0 {
			return nil, errors.New("sender lost address")
		}
		if c.mode == "primary_failure" {
			return nil, errors.New("private scheduled sender detail")
		}
		address := fmt.Sprint(args[0].Value)
		id := int64(20)
		if address == "user@example.com" {
			id = 10
		}
		if address == "linked@example.com" {
			id = 1
		}
		return &authReadSnapshotRows{columns: []string{"id", "address", "active"}, rows: [][]driver.Value{{id, address, true}}}, nil
	}
	if strings.Contains(query, `FROM "messages"`) {
		if len(args) == 0 || !strings.Contains(query, `"messages"."id" =`) {
			return nil, errors.New("attachment list lost message identity")
		}
		id := fmt.Sprint(args[0].Value)
		if id == "3" {
			return nil, errors.New("private attachment message detail")
		}
		r := &authReadSnapshotRows{columns: []string{"id", "mailbox_id"}}
		if id != "2147483647" {
			n, err := strconv.ParseInt(id, 10, 64)
			if err != nil {
				return nil, err
			}
			mailbox := n
			if id == "4" {
				mailbox = 1
			}
			if id == "5" {
				mailbox = 3
			}
			r.rows = [][]driver.Value{{n, mailbox}}
		}
		return r, nil
	}
	if strings.Contains(query, `FROM "attachments"`) {
		if len(args) == 0 || !strings.Contains(query, "message_id =") || !strings.Contains(query, "ORDER BY id ASC") {
			return nil, errors.New("attachment list lost message restriction or ordering")
		}
		id := fmt.Sprint(args[0].Value)
		if id == "4" {
			return nil, errors.New("private attachment metadata detail")
		}
		r := &authReadSnapshotRows{columns: []string{"id", "message_id", "filename", "content_type", "size_bytes", "checksum", "created_at", "storage_type", "storage_ref"}}
		if id != "2" {
			n, err := strconv.ParseInt(id, 10, 64)
			if err != nil {
				return nil, err
			}
			r.rows = [][]driver.Value{{int64(61), n, "résumé.pdf", "application/pdf", int64(123), strings.Repeat("a", 64), stamp, "filesystem", "private-storage-ref-sentinel"}, {int64(62), n, "", "application/octet-stream", int64(0), "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", stamp, "s3", "private-object-ref-sentinel"}}
		}
		return r, nil
	}
	return c.personalDataReadConnection.QueryContext(ctx, query, args)
}

func mailResourceReadScenarios(t *testing.T) map[string]accountReadScenario {
	t.Helper()
	extra := map[string]accountListKey{}
	tokens := map[string]string{}
	for name, scope := range map[string]string{"key_send": "mailbox:linked@example.com", "key_send_domain": "domain:example.com"} {
		token, public, hash, err := auth.NewAPIKey()
		if err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(models.CredentialScope{Permissions: []auth.ScopedPermission{{Capability: "mail:send", Scope: scope}}})
		if err != nil {
			t.Fatal(err)
		}
		extra[public] = accountListKey{accountReadKey{hash: hash, scope: data}, models.CredentialKindAPIKey}
		tokens[name] = token
	}
	out := accountListScenarios(t, func(mode string, keys map[string]accountListKey) driver.Connector {
		for public, key := range extra {
			keys[public] = key
		}
		return mailResourceReadConnection{personalDataReadConnection{accountCredentialListConnection{mode: mode, keys: keys}}}
	})
	out["mailbox_unavailable"] = out["found"]
	out["primary_only"] = out["no_grants"]
	out["empty_scheduled"] = out["primary_missing"]
	out["sender_unavailable"] = out["primary_unavailable"]
	out["domain_grant"] = out["domain_manager"]
	out["app_password_other"] = out["app_password"]
	for name, token := range tokens {
		out[name] = accountReadScenario{out["found"].handler, token}
	}
	out["key_send_holder_missing"] = accountReadScenario{out["write_only"].handler, tokens["key_send"]}
	return out
}

func TestAttachmentListFailuresAreNotMissingOrEmpty(t *testing.T) {
	scenarios := mailResourceReadScenarios(t)
	for _, tc := range []struct{ path, scenario string }{{"/api/v1/messages/3/attachments", "found"}, {"/api/v1/messages/5/attachments", "found"}, {"/api/v1/messages/1/attachments", "grant_unavailable"}, {"/api/v1/messages/1/attachments", "ownership_unavailable"}, {"/api/v1/messages/4/attachments", "found"}} {
		s := scenarios[tc.scenario]
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		req.Header.Set("Authorization", "Bearer "+s.token)
		rr := httptest.NewRecorder()
		s.handler.ServeHTTP(rr, req)
		if rr.Code != 500 || rr.Body.String() != "{\"error\":{\"code\":\"internal_error\",\"message\":\"Internal server error\"}}\n" {
			t.Errorf("%s/%s: got %d %s, want sanitized 500", tc.path, tc.scenario, rr.Code, rr.Body.String())
		}
	}
}
