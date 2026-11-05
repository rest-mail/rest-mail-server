package api

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/examplepairs"
)

type restmailAuthExample struct {
	name           string
	strict         bool
	local          bool
	trustedPeers   string
	trustedProxies []string
	headers        map[string]string
}

var restmailAuthExamples = []restmailAuthExample{
	{name: "default_local_unsigned_refused", local: true},
	{name: "default_external_unsigned_deferred"},
	{name: "strict_external_unsigned_refused", strict: true},
	{name: "strict_trusted_local_deferred", strict: true, local: true, trustedPeers: "127.0.0.1/32"},
	{name: "strict_trusted_external_deferred", strict: true, trustedPeers: "127.0.0.1/32"},
	{name: "untrusted_forwarded_peer_refused", strict: true, local: true, trustedPeers: "192.0.2.10/32", headers: map[string]string{"X-Forwarded-For": "192.0.2.10", "X-Real-IP": "192.0.2.10"}},
	{name: "trusted_proxy_untrusted_origin_refused", strict: true, local: true, trustedPeers: "127.0.0.1/32", trustedProxies: []string{"127.0.0.1/32"}, headers: map[string]string{"X-Forwarded-For": "192.0.2.20"}},
	{name: "trusted_proxy_trusted_origin_deferred", strict: true, local: true, trustedPeers: "192.0.2.10/32", trustedProxies: []string{"127.0.0.1/32"}, headers: map[string]string{"X-Forwarded-For": "192.0.2.10"}},
	{name: "default_local_raw_unsigned_refused", local: true},
	{name: "default_external_raw_unsigned_deferred"},
}

type restmailAuthConnection struct {
	capabilityExampleConnection
	mu             sync.Mutex
	local          bool
	senderReads    int
	recipientReads int
	unexpected     []string
}

func (connection *restmailAuthConnection) Connect(context.Context) (driver.Conn, error) {
	return connection, nil
}

func (connection *restmailAuthConnection) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	connection.mu.Lock()
	defer connection.mu.Unlock()
	if strings.Contains(query, `FROM "capabilities"`) {
		return connection.capabilityExampleConnection.QueryContext(ctx, query, args)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.Contains(query, `FROM "domains"`) && strings.Contains(query, "name =") && len(args) > 0 {
		domain, ok := args[0].Value.(string)
		if ok && (domain == "restmail.test" && connection.local || domain == "external.test" && !connection.local) {
			connection.senderReads++
			rows := &authReadSnapshotRows{columns: []string{"id", "name", "active"}}
			if connection.local {
				rows.rows = [][]driver.Value{{int64(42), domain, true}}
			}
			return rows, nil
		}
	}
	if strings.Contains(query, `FROM "mailboxes"`) && len(args) > 0 && args[0].Value == "eve@restmail.test" {
		connection.recipientReads++
		return nil, errors.New("private recipient lookup dependency detail")
	}
	connection.unexpected = append(connection.unexpected, query)
	return nil, errors.New("unexpected SQL in RESTMAIL authentication example")
}

func restmailAuthExampleRouter(t *testing.T, example restmailAuthExample, recipientReads int) http.Handler {
	t.Helper()
	t.Setenv("RESTMAIL_DELIVER_STRICT", "false")
	if example.strict {
		t.Setenv("RESTMAIL_DELIVER_STRICT", "true")
	}
	t.Setenv("RESTMAIL_DELIVER_TRUSTED_CIDRS", example.trustedPeers)
	connection := &restmailAuthConnection{local: example.local}
	pool := sql.OpenDB(connection)
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if err := pool.Close(); err != nil {
			t.Error(err)
		}
		connection.mu.Lock()
		defer connection.mu.Unlock()
		if connection.senderReads != 1 || connection.recipientReads != recipientReads || len(connection.unexpected) != 0 {
			t.Errorf("sender reads=%d, recipient reads=%d, unexpected=%v; want 1, %d, none", connection.senderReads, connection.recipientReads, connection.unexpected, recipientReads)
		}
	})
	routers := NewRouters(openAccountReadDB(t, pool), auth.NewJWTService(routerTestSecret, time.Minute, time.Hour), &config.Config{
		Environment: "test", ProxyProtocolTrustedCIDRs: example.trustedProxies,
	}, nil)
	t.Cleanup(routers.Close)
	return routers.Public
}

func testRestmailUnsignedAuthenticationExamples(t *testing.T, binary string) {
	t.Helper()
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	for _, example := range restmailAuthExamples {
		t.Run(example.name, func(t *testing.T) {
			pair, ok := pairs["restmailDeliver"][example.name]
			if !ok {
				t.Fatal("the named authentication pair is missing")
			}
			if pair.Requires["network"] != "restmail_delivery_auth" {
				t.Fatal("the pair does not declare its delivery-authentication network fixture")
			}
			examplepairs.Proven(t, "restmailDeliver", example.name)
			recipientReads := 0
			if pair.Response.Status == http.StatusServiceUnavailable {
				recipientReads = 1
			}
			handler := restmailAuthExampleRouter(t, example, recipientReads)
			if binary == "" {
				request := requirementRequest(requirementRoute{http.MethodPost, "/restmail/messages"}, pair)
				for name, value := range example.headers {
					request.Header.Set(name, value)
				}
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)
				assertRequirementResponse(t, pair, response)
				return
			}
			checkNamedPairWithVertrag(t, binary, handler, namedPairExchange{
				method: http.MethodPost, path: "/restmail/messages", uri: "/restmail/messages", name: example.name,
				status: pair.Response.Status, body: string(pair.Request.Body), headers: example.headers,
			})
		})
	}
}

func TestRestmailUnsignedAuthenticationExamples(t *testing.T) {
	testRestmailUnsignedAuthenticationExamples(t, "")
}

func TestRestmailUnsignedAuthenticationPairsFitTheDescription(t *testing.T) {
	generator := describedRouters(t).pairGenerator(t, "unsigned RESTMAIL authentication")
	for _, warning := range generator.Report() {
		if warning.OperationID == "restmailDeliver" {
			t.Error(warning)
		}
	}
}

func TestRestmailUnsignedAuthenticationExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY to replay RESTMAIL authentication examples")
	}
	testRestmailUnsignedAuthenticationExamples(t, binary)
}
