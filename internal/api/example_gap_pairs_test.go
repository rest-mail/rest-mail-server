package api

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/examplepairs"
)

type gapPairConnection struct {
	*authRequirementConnection
	operation string
	faults    int
	reads     int
	policy    int
	mutations int
}

func (connection *gapPairConnection) Connect(context.Context) (driver.Conn, error) {
	return connection, nil
}

func (connection *gapPairConnection) Begin() (driver.Tx, error) {
	if (connection.operation == "updateServerCapabilities" || connection.operation == "updateServerLimits" || connection.operation == "requestRecoveryEmail") && connection.faults == 0 {
		connection.faults++
		return nil, errors.New("injected transaction failure")
	}
	return bulkQueueExampleTx{}, nil
}

func (connection *gapPairConnection) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if !strings.Contains(query, "FROM audit_log") {
		connection.reads++
	}
	switch connection.operation {
	case "listClientSecrets", "createClientSecret", "revokeClientSecret":
		if strings.Contains(query, `FROM "mailboxes"`) && !strings.Contains(query, "owner_id") {
			connection.faults++
			return nil, errors.New("injected managed account lookup failure")
		}
	case "issueClientCredentialsToken":
		if strings.Contains(query, `FROM "credentials"`) {
			connection.faults++
			return nil, errors.New("injected client secret lookup failure")
		}
	case "saveDraft", "updateDraft", "sendDraft", "forwardMessage":
		if rows, ok := defaultServerCeilingRows(query, args); ok {
			rows.rows = [][]driver.Value{{"*", models.CapMailboxSession}}
			return rows, nil
		}
		if strings.Contains(query, `FROM "messages"`) {
			return importRows(map[string]any{"id": int64(12), "mailbox_id": int64(1), "is_draft": connection.operation != "forwardMessage",
				"sender": "admin@restmail.test", "recipients_to": `["someone@imap.restmail.test"]`}), nil
		}
	case "unsubscribe_not_allowed":
		if strings.Contains(query, `FROM "mailboxes"`) && !strings.Contains(query, "owner_id") {
			return importRows(map[string]any{"id": int64(1), "domain_id": int64(1), "address": "admin@restmail.test", "active": true}), nil
		}
		if strings.Contains(query, `FROM "domains"`) {
			if len(args) == 0 || args[0].Value != int64(1) {
				connection.t.Fatalf("sending policy did not select the sender's domain: %s %v", query, args)
			}
			connection.policy++
			return importRows(map[string]any{"sending_policy": models.SendingPolicyTransactional}), nil
		}
	}
	return connection.authRequirementConnection.QueryContext(ctx, query, args)
}

func (connection *gapPairConnection) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if !strings.HasPrefix(query, "SELECT pg_advisory_xact_lock") && !strings.HasPrefix(query, `INSERT INTO "audit_log"`) {
		connection.mutations++
		connection.t.Errorf("refused request attempted a non-audit write: %s", query)
		return nil, errors.New("unexpected mutation after refusal")
	}
	return connection.authRequirementConnection.ExecContext(ctx, query, args)
}

func newGapPairRouter(t *testing.T, operation string) (*Routers, *auth.JWTService, *gapPairConnection) {
	t.Helper()
	connection := &gapPairConnection{authRequirementConnection: &authRequirementConnection{t: t, password: requirementHash(t, "fixture-password")}, operation: operation}
	pool := sql.OpenDB(connection)
	t.Cleanup(func() { _ = pool.Close() })
	jwt := auth.NewJWTService(routerTestSecret, 5*time.Minute, time.Hour)
	routers := NewRouters(openAccountReadDB(t, pool), jwt, &config.Config{Environment: "test", RecoveryEmailBaseURL: "https://mail.example.test"}, nil)
	t.Cleanup(routers.Close)
	return routers, jwt, connection
}

func TestGapDatabaseExamplePairsThroughProductionRouter(t *testing.T) {
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	routes := requirementRoutes(t)
	for _, operation := range []string{"listClientSecrets", "createClientSecret", "revokeClientSecret", "updateServerCapabilities", "updateServerLimits", "issueClientCredentialsToken"} {
		t.Run(operation, func(t *testing.T) {
			pair := pairs[operation]["storage_unavailable"]
			examplepairs.Proven(t, operation, "storage_unavailable")
			routers, jwt, connection := newGapPairRouter(t, operation)
			request := requirementRequest(routes[operation], pair)
			if operation == "issueClientCredentialsToken" {
				var form string
				if err := json.Unmarshal(pair.Request.Body, &form); err != nil {
					t.Fatal(err)
				}
				request = httptest.NewRequest(routes[operation].method, routes[operation].path, strings.NewReader(form))
				request.Header.Set("Content-Type", pair.Request.MediaType)
			} else {
				session, err := jwt.GenerateSession(testAdminHolding([]string{"*"}), "admin@restmail.test", 0, []string{auth.AMRPassword, auth.AMROTP}, false)
				if err != nil {
					t.Fatal(err)
				}
				request.Header.Set("Authorization", "Bearer "+session.AccessToken)
			}
			recorder := httptest.NewRecorder()
			routers.Public.ServeHTTP(recorder, request)
			assertRequirementResponse(t, pair, recorder)
			if strings.Contains(recorder.Body.String(), "injected transaction failure") {
				t.Fatal("private storage detail escaped in the response")
			}
			if connection.faults != 1 {
				t.Fatalf("intended storage fault reached %d times, want 1", connection.faults)
			}
			if operation == "issueClientCredentialsToken" && (recorder.Header().Get("Cache-Control") != "no-store" || recorder.Header().Get("Pragma") != "no-cache") {
				t.Fatal("OAuth error lost its no-store headers")
			}
		})
	}
}

func TestGapCapabilityExamplePairsThroughProductionRouter(t *testing.T) {
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	routes := requirementRoutes(t)
	for _, operation := range []string{"saveDraft", "updateDraft", "sendDraft", "forwardMessage"} {
		t.Run(operation, func(t *testing.T) {
			pair := pairs[operation]["capability_removed"]
			examplepairs.Proven(t, operation, "capability_removed")
			routers, jwt, connection := newGapPairRouter(t, operation)
			session, err := jwt.GenerateSession(1, "admin@restmail.test", 0, []string{auth.AMRPassword}, false)
			if err != nil {
				t.Fatal(err)
			}
			request := requirementRequest(routes[operation], pair)
			request.Header.Set("Authorization", "Bearer "+session.AccessToken)
			recorder := httptest.NewRecorder()
			routers.Public.ServeHTTP(recorder, request)
			assertRequirementResponse(t, pair, recorder)
			if connection.reads == 0 || connection.mutations != 0 {
				t.Fatalf("refusal did not read fixture state or mutated it: reads=%d mutations=%d", connection.reads, connection.mutations)
			}
		})
	}
}

func TestRecoveryEmailStorageFailureRemainsPrivate(t *testing.T) {
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	routers, _, connection := newGapPairRouter(t, "requestRecoveryEmail")
	examplepairs.Proven(t, "requestRecoveryEmail", "database_unavailable")
	pair := pairs["requestRecoveryEmail"]["database_unavailable"]
	request := requirementRequest(requirementRoutes(t)["requestRecoveryEmail"], pair)
	recorder := httptest.NewRecorder()
	routers.Public.ServeHTTP(recorder, request)
	assertRequirementResponse(t, pair, recorder)
	if connection.faults != 1 {
		t.Fatalf("storage failure reached %d times, want 1", connection.faults)
	}
	if strings.Contains(recorder.Body.String(), "injected transaction failure") {
		t.Fatal("private storage detail escaped in the recovery response")
	}
}

// A recovery request answers its own storage and delivery failures with the
// private 200 above, so no database fault reaches its documented 500. That 500 is
// the endpoint layer's fallback for an unexpected error, which the shared adapter
// writes without the error's detail.
func TestRecoveryEmailFallbackFailureMatchesErrorAdapter(t *testing.T) {
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	examplepairs.Proven(t, "requestRecoveryEmail", "internal_error")
	pair, ok := pairs["requestRecoveryEmail"]["internal_error"]
	if !ok {
		t.Fatal("the named fault pair is missing")
	}
	recorder := httptest.NewRecorder()
	endpoint.WriteError(recorder, errors.New("dependency details must stay private"))
	assertRequirementResponse(t, pair, recorder)
	if strings.Contains(recorder.Body.String(), "dependency details") {
		t.Fatal("private failure detail escaped in the fallback response")
	}
}

func TestServerLimitsFailureExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY for server limits exchanges")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	routers, jwt, connection := newGapPairRouter(t, "updateServerLimits")
	session, err := jwt.GenerateSession(testAdminHolding([]string{"*"}), "admin@restmail.test", 0, []string{auth.AMRPassword, auth.AMROTP}, false)
	if err != nil {
		t.Fatal(err)
	}
	checkQueueMutationPairWithVertrag(t, binary, bulkQueueExampleScenario{accountReadScenario: accountReadScenario{handler: routers.Public, token: session.AccessToken}}, http.MethodPut, "/api/v1/admin/server-limits", "storage_unavailable", pairs["updateServerLimits"]["storage_unavailable"])
	if connection.faults != 1 {
		t.Fatalf("transaction failures = %d, want 1", connection.faults)
	}
}

func TestCorrectedSendMessageExamplePairsThroughProductionRouter(t *testing.T) {
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"from_missing", "unsubscribe_not_allowed"} {
		t.Run(name, func(t *testing.T) {
			pair := pairs["sendMessage"][name]
			examplepairs.Proven(t, "sendMessage", name)
			routers, jwt, connection := newGapPairRouter(t, name)
			session, err := jwt.GenerateSession(1, "admin@restmail.test", 0, []string{auth.AMRPassword}, false)
			if err != nil {
				t.Fatal(err)
			}
			request := requirementRequest(requirementRoutes(t)["sendMessage"], pair)
			request.Header.Set("Authorization", "Bearer "+session.AccessToken)
			recorder := httptest.NewRecorder()
			routers.Public.ServeHTTP(recorder, request)
			assertRequirementResponse(t, pair, recorder)
			if name == "from_missing" && connection.reads != 0 || name == "unsubscribe_not_allowed" && connection.policy != 1 || connection.mutations != 0 {
				t.Fatalf("wrong refusal point or mutation: reads=%d policy=%d writes=%v", connection.reads, connection.policy, connection.writes)
			}
		})
	}
}

func TestACMEExamplePairsThroughProductionRouter(t *testing.T) {
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range []string{"acmeChallenge", "headAcmeChallenge"} {
		for _, name := range []string{"issued_token", "unknown_token", "responder_unavailable"} {
			t.Run(operation+"/"+name, func(t *testing.T) {
				pair := pairs[operation][name]
				examplepairs.Proven(t, operation, name)
				routers, _, _ := newGapPairRouter(t, operation)
				calls := 0
				responder := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
					calls++
					if name == "responder_unavailable" {
						http.Error(writer, "responder failed", http.StatusServiceUnavailable)
						return
					}
					if request.URL.Path != "/.well-known/acme-challenge/issued-token" {
						http.NotFound(writer, request)
						return
					}
					writer.Header().Set("Content-Type", "text/plain")
					_, _ = writer.Write([]byte("issued-token.key-authorization"))
				})
				registerACME(endpoint.Routes{Router: routers.Public.(chi.Router), Registry: routers.Registry}, responder)
				method := http.MethodGet
				if operation == "headAcmeChallenge" {
					method = http.MethodHead
				}
				request := requirementRequest(requirementRoute{method, "/.well-known/acme-challenge/{token}"}, pair)
				server := httptest.NewServer(routers.Public)
				t.Cleanup(server.Close)
				request.RequestURI = ""
				request.URL.Scheme = "http"
				request.URL.Host = strings.TrimPrefix(server.URL, "http://")
				response, err := server.Client().Do(request)
				if err != nil {
					t.Fatal(err)
				}
				defer response.Body.Close()
				bodyBytes, err := io.ReadAll(response.Body)
				if err != nil {
					t.Fatal(err)
				}
				recorder := httptest.NewRecorder()
				recorder.Code = response.StatusCode
				for name, values := range response.Header {
					recorder.Header()[name] = values
				}
				_, _ = recorder.Body.Write(bodyBytes)
				if pair.Response.MediaType == "text/plain" {
					var body string
					if len(pair.Response.Body) > 0 {
						if err := json.Unmarshal(pair.Response.Body, &body); err != nil {
							t.Fatal(err)
						}
					}
					if recorder.Code != pair.Response.Status || recorder.Body.String() != body || !strings.HasPrefix(recorder.Header().Get("Content-Type"), pair.Response.MediaType) {
						t.Fatalf("got %d %q %q, want %d %q %q", recorder.Code, recorder.Header().Get("Content-Type"), recorder.Body.String(), pair.Response.Status, pair.Response.MediaType, body)
					}
				} else {
					assertRequirementResponse(t, pair, recorder)
				}
				if calls != 1 {
					t.Fatalf("responder called %d times, want 1", calls)
				}
			})
		}
	}
}
