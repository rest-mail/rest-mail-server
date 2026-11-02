package api

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/examplepairs"
)

func TestTLSReportAccessExamplePairs(t *testing.T) {
	fixture := newGrantsFixture(t, fixtureAdmin{name: "tls-domain"})
	if fixture.a.ID != 1 || fixture.b.ID != 2 {
		t.Fatalf("example domains = %d, %d; want 1, 2", fixture.a.ID, fixture.b.ID)
	}
	owner := fixture.token(t, "owner")
	if response := fixture.grant(t, owner, fixture.admins["tls-domain"].user.ID, "admin", "domain:a.test"); response.Code != http.StatusCreated {
		t.Fatalf("grant: %d %s", response.Code, response.Body)
	}
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	domainToken := fixture.token(t, "tls-domain")
	for name, token := range map[string]string{
		"domain_unfiltered": domainToken, "domain_own_filter": domainToken,
		"domain_other_filter": domainToken, "domain_missing_filter": domainToken,
		"global_unfiltered": fixture.token(t, "viewer"), "required_capability": fixture.token(t, "nobody"),
	} {
		t.Run(name, func(t *testing.T) {
			examplepairs.Proven(t, "listTlsReports", name)
			pair, ok := pairs["listTlsReports"][name]
			if !ok {
				t.Fatal("TLS report access pair missing")
			}
			query := url.Values{}
			for key, value := range pair.Request.Query {
				query.Set(key, fmt.Sprint(value))
			}
			response := doRequest(fixture.router, http.MethodGet, "/api/v1/admin/tls-reports?"+query.Encode(), token, "")
			if response.Code != pair.Response.Status || response.Header().Get("Content-Type") != pair.Response.MediaType {
				t.Fatalf("response = %d %s, want %d %s", response.Code, response.Body, pair.Response.Status, pair.Response.MediaType)
			}
			assertPublicExampleBody(t, pair.Response.Body, response.Body.Bytes())
		})
	}
}
