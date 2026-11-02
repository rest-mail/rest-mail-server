package api

import (
	"bytes"
	"context"
	"database/sql/driver"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/examplepairs"
)

type threadSnoozeConnection struct{ mailRemovalConnection }

func (connection threadSnoozeConnection) Connect(context.Context) (driver.Conn, error) {
	return connection, nil
}

func (connection threadSnoozeConnection) Driver() driver.Driver { return connection }

func (connection threadSnoozeConnection) Open(string) (driver.Conn, error) { return connection, nil }

func (connection threadSnoozeConnection) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.HasPrefix(query, `SELECT count(*) FROM "messages"`) {
		if len(args) != 2 || args[0].Value != int64(1) || !strings.Contains(query, "mailbox_id =") || !strings.Contains(query, "thread_id =") || !strings.Contains(query, "is_deleted = false") {
			return nil, errors.New("snooze lookup lost account, thread or deletion restriction")
		}
		count := int64(1)
		if args[1].Value == "missing" {
			count = 0
		}
		return &authReadSnapshotRows{columns: []string{"count"}, rows: [][]driver.Value{{count}}}, nil
	}
	return connection.mailRemovalConnection.QueryContext(ctx, query, args)
}

func (connection threadSnoozeConnection) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if !strings.HasPrefix(query, `INSERT INTO "thread_snoozes"`) || len(args) != 3 || args[0].Value != int64(1) || !strings.Contains(query, `ON CONFLICT ("mailbox_id","thread_id") DO UPDATE SET "snoozed_until"="excluded"."snoozed_until"`) {
		return nil, errors.New("snooze changed messages or lost account-scoped upsert")
	}
	if args[1].Value == "WriteFailure" {
		return nil, errors.New("private snooze database detail")
	}
	return driver.RowsAffected(1), nil
}

func threadSnoozeScenarios(t *testing.T) mailRemovalScenarios {
	t.Helper()
	fixture := newMailRulesWriteScenarios(t, func(base mailRemovalConnection) driver.Connector {
		return threadSnoozeConnection{base}
	}, 512)
	for _, name := range []string{"snoozed", "cancelled", "past", "malformed_time", "thread_missing", "write_unavailable"} {
		fixture.scenarios[name] = fixture.scenarios["found"]
	}
	return fixture
}

func TestThreadSnoozeExamples(t *testing.T) {
	fixture := threadSnoozeScenarios(t)
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs["snoozeThread"]) < 14 {
		t.Fatal("missing snooze examples")
	}
	for name, pair := range pairs["snoozeThread"] {
		t.Run(name, func(t *testing.T) {
			examplepairs.Proven(t, "snoozeThread", name)
			scenario, ok := fixture.scenarios[name]
			if !ok {
				t.Fatalf("missing scenario %s", name)
			}
			body := []byte(pair.Request.Body)
			if name == "malformed_json" {
				body = []byte("{")
			}
			if name == "request_too_large" {
				body = bytes.Repeat([]byte("x"), 513)
			}
			request := httptest.NewRequest(http.MethodPost, messageCollectionReadURI("/api/v1/accounts/{id}/threads/{threadID}/snooze", pair), bytes.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			if scenario.token != "" {
				request.Header.Set("Authorization", "Bearer "+scenario.token)
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
			if warning.OperationID == "snoozeThread" {
				t.Errorf("snooze description: %v", warning)
			}
		}
	}
}

func checkThreadSnoozeExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY for snooze exchanges")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	fixture := threadSnoozeScenarios(t)
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	for name, pair := range pairs["snoozeThread"] {
		t.Run(name, func(t *testing.T) {
			scenario, ok := fixture.scenarios[name]
			if !ok {
				t.Fatalf("missing scenario %s", name)
			}
			checkQueueMutationPairWithVertrag(t, binary, bulkQueueExampleScenario{scenario, nil}, http.MethodPost,
				"/api/v1/accounts/{id}/threads/{threadID}/snooze", name, pair, 513)
		})
	}
}
