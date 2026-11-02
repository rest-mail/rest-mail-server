package api

import (
	"context"
	"database/sql/driver"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/examplepairs"
)

// The owner and reset routes are not sent by the live contract run: it has no owner
// key to sign with and no reset token to redeem. Their pairs that need no setup are
// proven here instead, through the production router on a server that lists no owner
// or status keys. Any query other than the owner registry fails the request, so a
// refusal that is meant to come before the database proves it does.

type ownerNoKeysConnection struct{ capabilityExampleConnection }

func (c ownerNoKeysConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }
func (c ownerNoKeysConnection) Driver() driver.Driver                        { return c }
func (c ownerNoKeysConnection) Open(string) (driver.Conn, error)             { return c, nil }
func (c ownerNoKeysConnection) QueryContext(_ context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if rows, ok := defaultServerCeilingRows(q, args); ok {
		return rows, nil
	}
	if strings.Contains(q, `FROM "owner_keys"`) {
		return invitationRows([]string{"public_key"}), nil
	}
	return nil, errors.New("unexpected owner fixture query: " + q)
}

func TestOwnerRecoveryRefusalExamples(t *testing.T) {
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	routes := requirementRoutes(t)
	jwt := auth.NewJWTService(routerTestSecret, 15*time.Minute, 7*24*time.Hour)
	for _, tc := range []struct{ op, name string }{
		{"ownerChallenge", "no_keys"},
		{"ownerStatus", "no_keys"}, {"ownerStatus", "body_array"}, {"ownerStatus", "signature_not_base64"}, {"ownerStatus", "malformed_json"},
		{"ownerToken", "no_keys"}, {"ownerToken", "body_array"}, {"ownerToken", "purpose_invalid"}, {"ownerToken", "malformed_json"},
		{"resetAdmin", "body_array"}, {"resetAdmin", "client_invalid"}, {"resetAdmin", "malformed_json"},
	} {
		t.Run(tc.op+"/"+tc.name, func(t *testing.T) {
			examplepairs.Proven(t, tc.op, tc.name)
			pair, ok := pairs[tc.op][tc.name]
			if !ok {
				t.Fatalf("no example %s of %s", tc.name, tc.op)
			}
			r := NewRouters(requirementDB(t, ownerNoKeysConnection{}), jwt, &config.Config{Environment: "test", JWTSecret: routerTestSecret}, nil)
			t.Cleanup(r.Close)
			req := requirementRequest(routes[tc.op], pair)
			if tc.name == "malformed_json" {
				// The pair's body is the truncated JSON it stands for, sent as those bytes.
				req = httptest.NewRequest(routes[tc.op].method, routes[tc.op].path, strings.NewReader("{"))
				req.RemoteAddr = "127.0.0.1:12345"
				req.Header.Set("Content-Type", "application/json")
			}
			rr := httptest.NewRecorder()
			r.Public.ServeHTTP(rr, req)
			assertRequirementResponse(t, pair, rr)
		})
	}
}
