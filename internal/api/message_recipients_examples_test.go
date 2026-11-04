package api

import (
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

const (
	messageRecipientsPath  = "/api/v1/messages/{id}/recipients"
	messageRecipientsFault = "private recipient storage detail"
)

// messageRecipientsReads are the reads of one recipient list, in the order the handler
// makes them. The handler answers a failure of any of them with the same 500.
var messageRecipientsReads = []string{"message", "queue", "sender", "recipient", "copy"}

// messageRecipientsFaults says which read fails, and counts the times it did. The test's
// router reads it for each query, and a request runs one query at a time.
type messageRecipientsFaults struct {
	read  string
	count int
}

func (faults *messageRecipientsFaults) fail(read string) error {
	if faults.read != read {
		return nil
	}
	faults.count++
	return errors.New(messageRecipientsFault)
}

// messageRecipientsConnection serves a Sent message with one local recipient, with no
// queue row, so a request makes every read in messageRecipientsReads.
type messageRecipientsConnection struct {
	personalDataReadConnection
	faults *messageRecipientsFaults
}

func (connection messageRecipientsConnection) Connect(context.Context) (driver.Conn, error) {
	return connection, nil
}
func (connection messageRecipientsConnection) Driver() driver.Driver { return connection }
func (connection messageRecipientsConnection) Open(string) (driver.Conn, error) {
	return connection, nil
}

func (connection messageRecipientsConnection) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	switch {
	case strings.Contains(query, `FROM "outbound_queue"`):
		if err := connection.faults.fail("queue"); err != nil {
			return nil, err
		}
		return &authReadSnapshotRows{columns: []string{"id", "recipient", "status", "attempts", "last_attempt", "failure_category"}}, nil
	case strings.Contains(query, `count(*)`) && strings.Contains(query, `FROM "messages"`):
		if err := connection.faults.fail("copy"); err != nil {
			return nil, err
		}
		return &authReadSnapshotRows{columns: []string{"count"}, rows: [][]driver.Value{{int64(1)}}}, nil
	case strings.Contains(query, `FROM "messages"`):
		if err := connection.faults.fail("message"); err != nil {
			return nil, err
		}
		return &authReadSnapshotRows{
			columns: []string{"id", "mailbox_id", "folder", "message_id", "sender", "recipients_to", "recipients_cc", "received_at"},
			rows:    [][]driver.Value{{int64(1), int64(1), "Sent", "<sent@example.test>", accountReadAddress("1"), []byte(`["local@example.com"]`), []byte(`[]`), nil}},
		}, nil
	case strings.HasPrefix(query, `SELECT "id","address" FROM "mailboxes"`):
		if err := connection.faults.fail("sender"); err != nil {
			return nil, err
		}
		return &authReadSnapshotRows{columns: []string{"id", "address"}, rows: [][]driver.Value{{int64(1), accountReadAddress("1")}}}, nil
	case strings.Contains(query, `FROM "mailboxes"`) && strings.Contains(query, "address =") && len(args) > 0 && args[0].Value == "local@example.com":
		if err := connection.faults.fail("recipient"); err != nil {
			return nil, err
		}
		return &authReadSnapshotRows{columns: []string{"id", "address"}, rows: [][]driver.Value{{int64(7), "local@example.com"}}}, nil
	}
	return connection.personalDataReadConnection.QueryContext(ctx, query, args)
}

func messageRecipientsScenarios(t *testing.T) (map[string]accountReadScenario, *messageRecipientsFaults) {
	t.Helper()
	faults := &messageRecipientsFaults{}
	scenarios := accountListScenarios(t, func(mode string, keys map[string]accountListKey) driver.Connector {
		return messageRecipientsConnection{personalDataReadConnection{accountCredentialListConnection{mode: mode, keys: keys}}, faults}
	})
	return scenarios, faults
}

// The six failure pairs, each sent through the production router with its condition really
// set up. The 500 is sent once for each read the handler makes, so no read can answer it
// differently: a failed read must never look like a message with no recipients.
func TestMessageRecipientsFailureExamples(t *testing.T) {
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	scenarios, faults := messageRecipientsScenarios(t)
	send := func(t *testing.T, scenario accountReadScenario, name string) *httptest.ResponseRecorder {
		t.Helper()
		pair, ok := pairs["listMessageRecipients"][name]
		if !ok {
			t.Fatalf("the named pair %s is missing", name)
		}
		request := requirementRequest(requirementRoute{http.MethodGet, messageRecipientsPath}, pair)
		if name == "request_too_large" {
			request = httptest.NewRequest(http.MethodGet, request.URL.String(), strings.NewReader("oversized"))
		}
		if scenario.token != "" {
			request.Header.Set("Authorization", "Bearer "+scenario.token)
		}
		response := httptest.NewRecorder()
		scenario.handler.ServeHTTP(response, request)
		assertRequirementResponse(t, pair, response)
		return response
	}

	// With no read failing the same request succeeds, so the 500s below come from the fault.
	t.Run("no_fault", func(t *testing.T) {
		faults.read, faults.count = "", 0
		request := httptest.NewRequest(http.MethodGet, "/api/v1/messages/1/recipients", nil)
		request.Header.Set("Authorization", "Bearer "+scenarios["found"].token)
		response := httptest.NewRecorder()
		scenarios["found"].handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"address":"local@example.com","delivery":"local","state":"delivered"`) {
			t.Fatalf("got %d %s, want the local recipient delivered", response.Code, response.Body.String())
		}
	})
	for _, name := range []string{"unauthenticated", "setup_required", "request_too_large", "rate_limited", "session_unavailable"} {
		t.Run(name, func(t *testing.T) {
			examplepairs.Proven(t, "listMessageRecipients", name)
			scenario, ok := scenarios[name]
			if !ok {
				scenario = scenarios["found"]
			}
			faults.read, faults.count = "", 0
			response := send(t, scenario, name)
			if name == "rate_limited" && response.Header().Get("Retry-After") == "" {
				t.Fatal("the rate refusal has no Retry-After")
			}
		})
	}
	for _, read := range messageRecipientsReads {
		t.Run("storage_unavailable/"+read, func(t *testing.T) {
			examplepairs.Proven(t, "listMessageRecipients", "storage_unavailable")
			faults.read, faults.count = read, 0
			response := send(t, scenarios["found"], "storage_unavailable")
			if faults.count != 1 {
				t.Fatalf("the %s read failed %d times, want 1", read, faults.count)
			}
			if strings.Contains(response.Body.String(), messageRecipientsFault) {
				t.Fatal("private storage detail escaped in the response")
			}
		})
	}
}

func TestMessageRecipientsFailureExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY for message recipient exchanges")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	scenarios, faults := messageRecipientsScenarios(t)
	for _, name := range []string{"unauthenticated", "setup_required", "request_too_large", "rate_limited", "session_unavailable", "storage_unavailable"} {
		t.Run(name, func(t *testing.T) {
			scenario, ok := scenarios[name]
			if !ok {
				scenario = scenarios["found"]
			}
			faults.read, faults.count = "", 0
			if name == "storage_unavailable" {
				faults.read = "queue"
			}
			checkMailboxLookupWithVertrag(t, binary, scenario.handler, scenario.token, messageRecipientsPath, name, pairs["listMessageRecipients"][name])
			if name == "storage_unavailable" && faults.count != 1 {
				t.Fatalf("queue read failures = %d, want 1", faults.count)
			}
		})
	}
}
