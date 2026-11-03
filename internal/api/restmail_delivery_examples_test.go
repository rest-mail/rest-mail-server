package api

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/examplepairs"
)

// restmailDeliver's refusals of the request itself are sent through the production
// router on a database that fails every query: each is answered before the sender's
// domain or a recipient is looked up, so none reaches it (#643). The pairs that need a
// database fault, an oversized body or an unauthenticated peer are proven by the shared
// requirement tests.
func TestRestmailDeliveryRefusalExamples(t *testing.T) {
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	route := requirementRoutes(t)["restmailDeliver"]
	r := NewRouters(newFailingGormDB(t), auth.NewJWTService(routerTestSecret, time.Minute, time.Hour), &config.Config{Environment: "test"}, nil)
	t.Cleanup(r.Close)
	for _, name := range []string{"malformed_json", "body_array", "recipients_missing", "raw_message_wrong_type"} {
		t.Run(name, func(t *testing.T) {
			examplepairs.Proven(t, "restmailDeliver", name)
			pair, ok := pairs["restmailDeliver"][name]
			if !ok {
				t.Fatal("the named pair is missing")
			}
			req := requirementRequest(route, pair)
			if pair.Requires["body"] == "malformed" {
				// The pair's body is the wire bytes, held as a JSON string.
				var raw string
				if err := json.Unmarshal(pair.Request.Body, &raw); err != nil {
					t.Fatal(err)
				}
				req = httptest.NewRequest(route.method, route.path, strings.NewReader(raw))
				req.RemoteAddr = "127.0.0.1:12345"
				req.Header.Set("Content-Type", "application/json")
			}
			rr := httptest.NewRecorder()
			r.Public.ServeHTTP(rr, req)
			assertRequirementResponse(t, pair, rr)
		})
	}

	// Delivery answers its own dependency failures 503 deferred, so the sending server
	// retries. The documented 500 is the endpoint layer's fallback for an unexpected
	// error, which the shared adapter writes without the error's detail.
	t.Run("internal_error", func(t *testing.T) {
		examplepairs.Proven(t, "restmailDeliver", "internal_error")
		pair, ok := pairs["restmailDeliver"]["internal_error"]
		if !ok {
			t.Fatal("the named fault pair is missing")
		}
		rr := httptest.NewRecorder()
		endpoint.WriteError(rr, errors.New("dependency details must stay private"))
		assertRequirementResponse(t, pair, rr)
	})
}
