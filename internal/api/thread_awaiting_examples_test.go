package api

import (
	"database/sql/driver"
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
	"github.com/rest-mail/rest-mail-server"
	"github.com/rest-mail/rest-mail-server/internal/examplepairs"
)

const listThreadsPath = "/api/v1/accounts/{id}/threads"

// awaitingThreadRows answers the waiting-thread query from fixed rows. They prove the route's
// wiring, its cursor and its bound; thread_awaiting_test.go proves the derivation on PostgreSQL.
func awaitingThreadRows(query string, args []driver.NamedValue) (driver.Rows, error) {
	if !strings.Contains(query, "messages.mailbox_id =") || !strings.Contains(query, "messages.is_deleted = false") || !strings.Contains(query, "NOT EXISTS") {
		return nil, errors.New("waiting threads lost their account or their derivation")
	}
	if strings.Contains(query, "messages.thread_id IN") {
		return &authReadSnapshotRows{columns: []string{"thread_id", "received_at"}}, nil
	}
	if !strings.Contains(query, "ORDER BY messages.received_at DESC, messages.id DESC") || !strings.Contains(query, "LIMIT") {
		return nil, errors.New("waiting threads lost their order or bound")
	}
	account := fmt.Sprint(args[0].Value)
	if args[1].Value != "linked@example.com" {
		return nil, fmt.Errorf("waiting threads compared the sender with %v", args[1].Value)
	}
	if account == "4" {
		return nil, errors.New("private waiting thread detail")
	}
	columns := []string{"id", "mailbox_id", "folder", "message_id", "in_reply_to", "references", "thread_id", "sender", "sender_name", "recipients_to", "recipients_cc", "subject", "body_text", "headers", "size_bytes", "raw_size", "has_attachments", "is_read", "is_dsn", "is_flagged", "is_starred", "is_draft", "is_deleted", "received_at", "date_header", "created_at", "updated_at"}
	rows := &authReadSnapshotRows{columns: columns}
	if account == "2" {
		return rows, nil
	}
	ids := []int64{72, 71}
	if strings.Contains(query, "(messages.received_at, messages.id) <") {
		if len(args) != 5 || args[3].Value != int64(72) {
			return nil, errors.New("waiting threads lost the cursor's date and id")
		}
		ids = ids[1:]
	}
	limit, ok := args[len(args)-1].Value.(int64)
	if !ok || (limit != 2 && limit != 51) {
		return nil, errors.New("waiting threads lost limit plus lookahead")
	}
	if int64(len(ids)) > limit {
		ids = ids[:limit]
	}
	mailbox, err := strconv.ParseInt(account, 10, 64)
	if err != nil {
		return nil, err
	}
	stamp := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	for _, id := range ids {
		thread := fmt.Sprintf("<thread-%d@example.com>", id)
		rows.rows = append(rows.rows, []driver.Value{id, mailbox, "Sent", fmt.Sprintf("<message-%d@example.com>", id), "", "", thread, "linked@example.com", "Linked", []byte(`["recipient@example.net"]`), []byte(`[]`), "Any news?", "Any news?", nil, int64(18), int64(0), false, true, false, false, false, false, false, stamp.Add(time.Duration(id-70) * time.Hour), nil, stamp, stamp})
	}
	return rows, nil
}

func TestListThreadsExamples(t *testing.T) {
	scenarios := messageCollectionReadScenarios(t)
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs["listThreads"]) < 15 {
		t.Fatal("missing waiting thread examples")
	}
	for name, pair := range pairs["listThreads"] {
		t.Run(name, func(t *testing.T) {
			examplepairs.Proven(t, "listThreads", name)
			scenario, ok := scenarios[name]
			if !ok {
				t.Fatalf("missing scenario %s", name)
			}
			request := httptest.NewRequest(http.MethodGet, messageCollectionReadURI(listThreadsPath, pair), nil)
			if scenario.token != "" {
				request.Header.Set("Authorization", "Bearer "+scenario.token)
			}
			if name == "request_too_large" {
				request.ContentLength = 9
			}
			recorder := httptest.NewRecorder()
			scenario.handler.ServeHTTP(recorder, request)
			if recorder.Code != pair.Response.Status {
				t.Fatalf("got %d %s, want %d", recorder.Code, recorder.Body, pair.Response.Status)
			}
			assertPublicExampleBody(t, pair.Response.Body, recorder.Body.Bytes())
		})
	}
	for _, generator := range gapGenerators(t) {
		for _, warning := range generator.Report() {
			if warning.OperationID == "listThreads" {
				t.Errorf("waiting thread description: %v", warning)
			}
		}
	}
}

func checkListThreadsExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY for waiting thread exchanges")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	scenarios := messageCollectionReadScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for name, pair := range pairs["listThreads"] {
		t.Run(name, func(t *testing.T) {
			scenario, ok := scenarios[name]
			if !ok {
				t.Fatalf("missing scenario %s", name)
			}
			checkMailboxLookupWithVertrag(t, binary, scenario.handler, scenario.token, listThreadsPath, name, pair)
		})
	}
}
