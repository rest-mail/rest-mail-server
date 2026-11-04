package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/examplepairs"
)

// A domain's own administrator, even one granted queue:manage on that domain, may
// not stop or restart the domain's outbound mail (#1590): the stop needs
// queue:manage at the server's own scope. The refusal comes before the domain is
// read or changed, so the published domain_scope pairs are proven here; the live
// run has no such principal.
func TestDomainOutboundStopDomainScopeExamples(t *testing.T) {
	s := bulkQueueExampleScenarios(t)["domain_scope"]
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	for operation, method := range map[string]string{"stopDomainOutbound": http.MethodPut, "restartDomainOutbound": http.MethodDelete} {
		t.Run(operation, func(t *testing.T) {
			examplepairs.Proven(t, operation, "domain_scope")
			pair, ok := pairs[operation]["domain_scope"]
			if !ok {
				t.Fatalf("%s publishes no domain_scope pair", operation)
			}
			before := len(s.store.snapshot())
			req := httptest.NewRequest(method, messageCollectionReadURI("/api/v1/admin/domains/{id}/outbound-stop", pair), bytes.NewReader(pair.Request.Body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+s.token)
			rr := httptest.NewRecorder()
			s.handler.ServeHTTP(rr, req)
			if rr.Code != pair.Response.Status || rr.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("got %d %s, want %d", rr.Code, rr.Body.String(), pair.Response.Status)
			}
			assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
			if after := len(s.store.snapshot()); after != before {
				t.Fatalf("a refused stop wrote %d times", after-before)
			}
		})
	}
}
