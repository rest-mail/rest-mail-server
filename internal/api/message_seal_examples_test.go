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
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/examplepairs"
)

const normalizedReceiptHash = "c8df0e809a88268049a3d046bf4c17819b16cbf2e5d3845e15368a251710bfb2"

func assertExampleReceipt(t *testing.T, data map[string]any, source string, storedSeal any) {
	t.Helper()
	encoded, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	var message models.Message
	if err := json.Unmarshal(encoded, &message); err != nil {
		t.Fatal(err)
	}
	message.RawMessage = source
	digest, err := message.SealDigest()
	if err != nil || message.SealedHash == "" || message.SealedHash != storedSeal || digest != message.SealedHash {
		t.Fatalf("answered receipt does not verify against stored source/content: answered %q, stored %v, computed %q, error %v", message.SealedHash, storedSeal, digest, err)
	}
}

type messageSealConnection struct {
	personalDataReadConnection
	faults *int
}

func (connection messageSealConnection) Connect(context.Context) (driver.Conn, error) {
	return connection, nil
}
func (connection messageSealConnection) Driver() driver.Driver            { return connection }
func (connection messageSealConnection) Open(string) (driver.Conn, error) { return connection, nil }

func (connection messageSealConnection) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if !strings.Contains(query, `FROM "messages"`) {
		return connection.personalDataReadConnection.QueryContext(ctx, query, args)
	}
	if len(args) != 2 || !strings.Contains(query, `"messages"."id" =`) || args[1].Value != int64(1) {
		return nil, errors.New("seal lookup lost message identity or bound")
	}
	rows := &authReadSnapshotRows{columns: []string{"id", "mailbox_id", "sender", "subject", "body_text", "raw_message", "message_id", "sealed_hash"}}
	id := fmt.Sprint(args[0].Value)
	if id == "5" {
		*connection.faults++
		return nil, errors.New("private seal storage detail")
	}
	if id == "2147483647" {
		return rows, nil
	}
	message := models.Message{MailboxID: 1, Sender: "sender@example.test", Subject: "Receipt", BodyText: "original body", MsgID: "<seal@example.test>", RawMessage: "Subject: Receipt\r\n\r\noriginal body\r\n"}
	seal, err := message.SealDigest()
	if err != nil {
		return nil, err
	}
	switch id {
	case "2":
		message.RawMessage = strings.Replace(message.RawMessage, "original", "changed", 1)
	case "3":
		seal = ""
	case "4":
		message.MailboxID = 20
	}
	rows.rows = [][]driver.Value{{int64(1), int64(message.MailboxID), message.Sender, message.Subject, message.BodyText, message.RawMessage, message.MsgID, seal}}
	return rows, nil
}

func messageSealScenarios(t *testing.T) (map[string]accountReadScenario, *int) {
	t.Helper()
	faults := new(int)
	scenarios := accountListScenarios(t, func(mode string, keys map[string]accountListKey) driver.Connector {
		return messageSealConnection{personalDataReadConnection{accountCredentialListConnection{mode: mode, keys: keys}}, faults}
	})
	scenarios["storage_unavailable"] = scenarios["found"]
	return scenarios, faults
}

func TestMessageSealNamedExamples(t *testing.T) {
	scenarios, faults := messageSealScenarios(t)
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs["verifyMessageSeal"]) != 11 {
		t.Fatal("missing named receipt examples")
	}
	for name, pair := range pairs["verifyMessageSeal"] {
		t.Run(name, func(t *testing.T) {
			examplepairs.Proven(t, "verifyMessageSeal", name)
			scenario := scenarios["found"]
			if selected, ok := scenarios[name]; ok {
				scenario = selected
			}
			request := httptest.NewRequest(http.MethodGet, messageCollectionReadURI("/api/v1/messages/{id}/seal", pair), nil)
			if name == "request_too_large" {
				request = httptest.NewRequest(http.MethodGet, request.URL.String(), strings.NewReader("oversized"))
			}
			if scenario.token != "" {
				request.Header.Set("Authorization", "Bearer "+scenario.token)
			}
			response := httptest.NewRecorder()
			scenario.handler.ServeHTTP(response, request)
			if response.Code != pair.Response.Status || response.Header().Get("Content-Type") != pair.Response.MediaType {
				t.Fatalf("HTTP %d %s, want %d", response.Code, response.Body.String(), pair.Response.Status)
			}
			assertPublicExampleBody(t, pair.Response.Body, response.Body.Bytes())
			if strings.Contains(response.Body.String(), "private seal storage detail") {
				t.Fatal("private storage detail escaped in the response")
			}
			if name == "storage_unavailable" && *faults != 1 {
				t.Fatalf("message lookup failures = %d, want 1", *faults)
			}
		})
	}
	for _, name := range []string{"other_account", "key_wrong_scope", "key_write_only", "no_grants"} {
		t.Run(name, func(t *testing.T) {
			scenario := scenarios[name]
			path := "/api/v1/messages/1/seal"
			if name == "other_account" {
				scenario = scenarios["found"]
				path = "/api/v1/messages/4/seal"
			}
			request := httptest.NewRequest(http.MethodGet, path, nil)
			request.Header.Set("Authorization", "Bearer "+scenario.token)
			response := httptest.NewRecorder()
			scenario.handler.ServeHTTP(response, request)
			if response.Code != http.StatusNotFound {
				t.Fatalf("missing mail.read right exposed receipt: %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestMessageSealExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY for message seal exchanges")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	scenarios, faults := messageSealScenarios(t)
	for name, pair := range pairs["verifyMessageSeal"] {
		t.Run(name, func(t *testing.T) {
			scenario := scenarios["found"]
			if selected, ok := scenarios[name]; ok {
				scenario = selected
			}
			checkMailboxLookupWithVertrag(t, binary, scenario.handler, scenario.token, "/api/v1/messages/{id}/seal", name, pair)
			if name == "storage_unavailable" && *faults != 1 {
				t.Fatalf("message lookup failures = %d, want 1", *faults)
			}
		})
	}
}
