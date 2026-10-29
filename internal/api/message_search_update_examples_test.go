package api

import (
	"bytes"
	"encoding/json"
	"github.com/rest-mail/rest-mail-server/internal/examplepairs"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
)

const (
	messageSearchPath = "/api/v1/accounts/{id}/search"
	messageUpdatePath = "/api/v1/messages/{id}"
)

// The refusals of searching and updating mail, sent through the production router.
// A found search needs PostgreSQL full-text matching and a changed message a stored
// row to write, so TestMessageSearchUpdateStoredExamples proves those pairs.
func TestMessageSearchRefusalExamples(t *testing.T) {
	scenarios := messageCollectionReadScenarios(t)
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"invalid_id", "missing", "setup_required"} {
		pair, ok := pairs["searchMessages"][name]
		if !ok {
			t.Fatalf("missing searchMessages pair %s", name)
		}
		t.Run(name, func(t *testing.T) {
			examplepairs.Proven(t, "searchMessages", name)
			s := scenarios[name]
			req := httptest.NewRequest(http.MethodGet, messageCollectionReadURI(messageSearchPath, pair), nil)
			req.Header.Set("Authorization", "Bearer "+s.token)
			rr := httptest.NewRecorder()
			s.handler.ServeHTTP(rr, req)
			if rr.Code != pair.Response.Status || rr.Header().Get("Content-Type") != pair.Response.MediaType {
				t.Fatalf("got %d %s %s, want %d %s", rr.Code, rr.Header().Get("Content-Type"), rr.Body.String(), pair.Response.Status, pair.Response.MediaType)
			}
			assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
		})
	}
}

func TestMessageUpdateRefusalExamples(t *testing.T) {
	f := newMessageRemovalScenarios(t)
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"invalid_id", "body_array", "malformed_json", "missing"} {
		pair, ok := pairs["updateMessage"][name]
		if !ok {
			t.Fatalf("missing updateMessage pair %s", name)
		}
		t.Run(name, func(t *testing.T) {
			examplepairs.Proven(t, "updateMessage", name)
			s := f.scenarios["found"]
			before := len(f.store.snapshot())
			body := pair.Request.Body
			if name == "malformed_json" {
				// The pair publishes the truncated byte as a JSON string; the wire carries it bare.
				body = []byte("{")
			}
			req := httptest.NewRequest(http.MethodPatch, messageCollectionReadURI(messageUpdatePath, pair), bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+s.token)
			rr := httptest.NewRecorder()
			s.handler.ServeHTTP(rr, req)
			if rr.Code != pair.Response.Status || rr.Header().Get("Content-Type") != pair.Response.MediaType {
				t.Fatalf("got %d %s %s, want %d %s", rr.Code, rr.Header().Get("Content-Type"), rr.Body.String(), pair.Response.Status, pair.Response.MediaType)
			}
			assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
			for _, call := range f.store.snapshot()[before:] {
				if strings.HasPrefix(call.query, "UPDATE") {
					t.Fatalf("refused update wrote: %s", call.query)
				}
			}
		})
	}
}

// A found search and a changed message, sent through the production router to a scratch
// PostgreSQL database: the search is PostgreSQL full-text matching, and the change is
// answered with the row read back. Only the allocated account and mailbox ids and the
// clock of the change are replaced with the published values.
func TestMessageSearchUpdateStoredExamples(t *testing.T) {
	if os.Getenv("DB_HOST") == "" {
		t.Skip("set DB_HOST for PostgreSQL production-router search and update proofs")
	}
	useRealGrants(t)
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	stamp := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	for _, c := range []struct{ operation, name, method, path string }{
		{"searchMessages", "found", http.MethodGet, messageSearchPath},
		{"updateMessage", "flags_set", http.MethodPatch, messageUpdatePath},
	} {
		t.Run(c.operation+"/"+c.name, func(t *testing.T) {
			examplepairs.Proven(t, c.operation, c.name)
			pair, ok := pairs[c.operation][c.name]
			if !ok || len(pair.Response.Body) == 0 {
				t.Fatal("the named request/response pair is incomplete")
			}
			f := newCredentialFixture(t)
			seed := models.Message{ID: 1, MailboxID: f.mailboxID, Folder: "INBOX", MsgID: "<message@example.net>",
				Sender: "sender@example.net", SenderName: "Sender", RecipientsTo: models.JSONB(`["recipient@example.com"]`),
				RecipientsCc: models.JSONB(`[]`), Subject: "Subject", Headers: models.JSONB(`{"Subject":["Subject"]}`),
				SizeBytes: 80, RawSize: 137, DowngradedSize: 137, ReceivedAt: stamp, DateHeader: &stamp, CreatedAt: stamp, UpdatedAt: stamp}
			if err := f.db.Create(&seed).Error; err != nil {
				t.Fatal(err)
			}
			path := c.path
			if c.operation == "searchMessages" {
				// The search names the account, whose id the scratch database allocates.
				path = strings.Replace(path, "{id}", strconv.FormatUint(uint64(f.accountID), 10), 1)
			}
			since := time.Now()
			rr := f.do(c.method, messageCollectionReadURI(path, pair), f.session, string(pair.Request.Body))
			if rr.Code != pair.Response.Status || rr.Header().Get("Content-Type") != pair.Response.MediaType {
				t.Fatalf("got %d %s %s, want %d %s", rr.Code, rr.Header().Get("Content-Type"), rr.Body.String(), pair.Response.Status, pair.Response.MediaType)
			}
			changed := c.operation == "updateMessage"
			var stored models.Message
			if err := f.db.First(&stored, seed.ID).Error; err != nil {
				t.Fatal(err)
			}
			if stored.IsRead != changed || stored.IsFlagged != changed || stored.IsStarred || stored.IsDraft || stored.Folder != "INBOX" {
				t.Fatalf("stored flags and folder: %+v", stored)
			}
			var actual, expected map[string]any
			if err := json.Unmarshal(rr.Body.Bytes(), &actual); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(pair.Response.Body, &expected); err != nil {
				t.Fatal(err)
			}
			got, want := actual["data"], expected["data"]
			if list, ok := got.([]any); ok {
				published, _ := want.([]any)
				if len(list) != 1 || len(published) != 1 {
					t.Fatalf("got %s, want one message", rr.Body.String())
				}
				got, want = list[0], published[0]
			}
			normalizeStoredMessageAnswer(t, got, want, f.mailboxID, stamp, changed, since)
			body, err := json.Marshal(actual)
			if err != nil {
				t.Fatal(err)
			}
			assertPublicExampleBody(t, pair.Response.Body, body)
		})
	}
}

// normalizeStoredMessageAnswer checks an answered message's mailbox id and stamps against
// storage, then gives them the published values. A changed message's updated_at is the
// time of the change; every other stamp is the seeded one.
func normalizeStoredMessageAnswer(t *testing.T, got, want any, mailboxID uint, stamp time.Time, changed bool, since time.Time) {
	t.Helper()
	data, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("answered message is not an object: %v", got)
	}
	published, ok := want.(map[string]any)
	if !ok {
		t.Fatalf("published message is not an object: %v", want)
	}
	if data["mailbox_id"] != float64(mailboxID) {
		t.Fatalf("mailbox_id %v, want the stored %d", data["mailbox_id"], mailboxID)
	}
	data["mailbox_id"] = published["mailbox_id"]
	for _, field := range []string{"received_at", "date_header", "created_at", "updated_at"} {
		text, _ := data[field].(string)
		at, err := time.Parse(time.RFC3339Nano, text)
		if err != nil {
			t.Fatalf("%s %v: %v", field, data[field], err)
		}
		if field == "updated_at" && changed {
			if at.Before(since.Add(-time.Minute)) || at.After(time.Now().Add(time.Second)) {
				t.Fatalf("updated_at %s is not the time of the change", text)
			}
		} else if !at.Equal(stamp) {
			t.Fatalf("%s %s, want the seeded %s", field, text, stamp.Format(time.RFC3339))
		}
		data[field] = published[field]
	}
}
