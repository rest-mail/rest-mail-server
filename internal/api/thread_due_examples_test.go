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

type threadDueConnection struct{ mailRemovalConnection }

func (connection threadDueConnection) Connect(context.Context) (driver.Conn, error) {
	return connection, nil
}
func (connection threadDueConnection) Driver() driver.Driver            { return connection }
func (connection threadDueConnection) Open(string) (driver.Conn, error) { return connection, nil }

func (connection threadDueConnection) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.HasPrefix(query, `SELECT count(*) FROM "messages"`) {
		if len(args) != 2 || args[0].Value != int64(1) || !strings.Contains(query, "mailbox_id =") || !strings.Contains(query, "thread_id =") || !strings.Contains(query, "is_deleted = false") {
			return nil, errors.New("due lookup lost account/thread/deletion restriction")
		}
		count := int64(1)
		if args[1].Value == "missing" {
			count = 0
		}
		return &authReadSnapshotRows{columns: []string{"count"}, rows: [][]driver.Value{{count}}}, nil
	}
	return connection.mailRemovalConnection.QueryContext(ctx, query, args)
}

func (connection threadDueConnection) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if !strings.HasPrefix(query, `INSERT INTO "thread_dues"`) || len(args) != 4 || args[0].Value != int64(1) || !strings.Contains(query, `ON CONFLICT ("mailbox_id","thread_id") DO UPDATE SET "due"="excluded"."due","due_source"="excluded"."due_source"`) {
		return nil, errors.New("due wrote messages or lost account-scoped upsert")
	}
	if args[1].Value == "WriteFailure" {
		return nil, errors.New("private due database detail")
	}
	if (args[2].Value == nil) != (args[3].Value == nil) || (args[3].Value != nil && args[3].Value != "user") {
		return nil, errors.New("due lost paired user source")
	}
	return driver.RowsAffected(1), nil
}

func threadDueScenarios(t *testing.T) mailRemovalScenarios {
	t.Helper()
	fixture := newMailRulesWriteScenarios(t, func(base mailRemovalConnection) driver.Connector { return threadDueConnection{base} }, 512)
	for _, name := range []string{"set", "clear", "source_rule", "source_parsed", "malformed_date", "thread_missing", "write_unavailable"} {
		fixture.scenarios[name] = fixture.scenarios["found"]
	}
	return fixture
}

func TestThreadDueExamples(t *testing.T) {
	fixture := threadDueScenarios(t)
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs["setThreadDue"]) < 15 {
		t.Fatal("missing due examples")
	}
	for name, pair := range pairs["setThreadDue"] {
		t.Run(name, func(t *testing.T) {
			examplepairs.Proven(t, "setThreadDue", name)
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
			request := httptest.NewRequest(http.MethodPost, messageCollectionReadURI("/api/v1/accounts/{id}/threads/{threadID}/due", pair), bytes.NewReader(body))
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
			if warning.OperationID == "setThreadDue" {
				t.Errorf("due description: %v", warning)
			}
		}
	}
}

func checkThreadDueExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY for due exchanges")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	fixture := threadDueScenarios(t)
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	for name, pair := range pairs["setThreadDue"] {
		t.Run(name, func(t *testing.T) {
			scenario, ok := fixture.scenarios[name]
			if !ok {
				t.Fatalf("missing scenario %s", name)
			}
			checkQueueMutationPairWithVertrag(t, binary, bulkQueueExampleScenario{scenario, nil}, http.MethodPost,
				"/api/v1/accounts/{id}/threads/{threadID}/due", name, pair, 513)
		})
	}
}
