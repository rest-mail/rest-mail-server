package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
)

func TestTLSReportDomainAccess(t *testing.T) {
	fixture := newGrantsFixture(t, fixtureAdmin{name: "tls-domain"})
	owner := fixture.token(t, "owner")
	if response := fixture.grant(t, owner, fixture.admins["tls-domain"].user.ID, "admin", "domain:a.test"); response.Code != http.StatusCreated {
		t.Fatalf("grant: %d %s", response.Code, response.Body)
	}
	now := time.Now().UTC().Truncate(time.Second)
	reports := []models.TLSReport{
		{DomainID: fixture.a.ID, PolicyDomain: fixture.a.Name, PolicyType: "sts", ReportingOrg: "Reporter A", ReceivedAt: now, StartDate: now.Add(-time.Hour), EndDate: now},
		{DomainID: fixture.a.ID, PolicyDomain: fixture.a.Name, PolicyType: "tlsa", ReportingOrg: "Reporter A", ReceivedAt: now.Add(time.Minute), StartDate: now.Add(-time.Hour), EndDate: now},
		{DomainID: fixture.b.ID, PolicyDomain: fixture.b.Name, PolicyType: "sts", ReportingOrg: "Reporter B", ReceivedAt: now.Add(2 * time.Minute), StartDate: now.Add(-time.Hour), EndDate: now},
	}
	if err := fixture.db.Create(&reports).Error; err != nil {
		t.Fatal(err)
	}
	request := func(token, query string) *httptest.ResponseRecorder {
		return doRequest(fixture.router, http.MethodGet, "/api/v1/admin/tls-reports"+query, token, "")
	}
	pathA := "?domain_id=" + strconv.FormatUint(uint64(fixture.a.ID), 10)
	pathB := "?domain_id=" + strconv.FormatUint(uint64(fixture.b.ID), 10)
	domainToken := fixture.token(t, "tls-domain")
	globalToken := fixture.token(t, "viewer")
	for _, scenario := range []struct {
		name, token, query string
		ids                []uint
		total              int
		hasMore            bool
	}{
		{"domain_unfiltered", domainToken, "", []uint{reports[1].ID, reports[0].ID}, 2, false},
		{"domain_own_filter", domainToken, pathA, []uint{reports[1].ID, reports[0].ID}, 2, false},
		{"domain_other_filter", domainToken, pathB, nil, 0, false},
		{"domain_missing_filter", domainToken, "?domain_id=4294967295", nil, 0, false},
		{"domain_first_page", domainToken, "?limit=1", []uint{reports[1].ID}, 2, true},
		{"domain_second_page", domainToken, "?limit=1&offset=1", []uint{reports[0].ID}, 2, false},
		{"domain_policy_filter", domainToken, "?policy_type=sts", []uint{reports[0].ID}, 1, false},
		{"domain_other_reporter", domainToken, "?reporting_org=Reporter+B", nil, 0, false},
		{"global_unfiltered", globalToken, "", []uint{reports[2].ID, reports[1].ID, reports[0].ID}, 3, false},
		{"global_own_filter", globalToken, pathA, []uint{reports[1].ID, reports[0].ID}, 2, false},
		{"global_other_filter", globalToken, pathB, []uint{reports[2].ID}, 1, false},
		{"global_missing_filter", globalToken, "?domain_id=4294967295", nil, 0, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			response := request(scenario.token, scenario.query)
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", response.Code, response.Body)
			}
			var body struct {
				Data       []models.TLSReport `json:"data"`
				Pagination struct {
					Total   int  `json:"total"`
					HasMore bool `json:"has_more"`
				} `json:"pagination"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			var ids []uint
			for _, report := range body.Data {
				ids = append(ids, report.ID)
				if report.Domain.ID != report.DomainID {
					t.Fatalf("report domain not preloaded: %+v", report)
				}
			}
			if !slices.Equal(ids, scenario.ids) || body.Pagination.Total != scenario.total || body.Pagination.HasMore != scenario.hasMore {
				t.Fatalf("IDs = %v, pagination = %+v; want %v, total %d, has_more %v", ids, body.Pagination, scenario.ids, scenario.total, scenario.hasMore)
			}
		})
	}
	other, missing := request(domainToken, pathB), request(domainToken, "?domain_id=4294967295")
	if other.Code != missing.Code || other.Body.String() != missing.Body.String() {
		t.Fatalf("other and missing domains differ: %d %s / %d %s", other.Code, other.Body, missing.Code, missing.Body)
	}
	nobody := fixture.token(t, "nobody")
	for _, query := range []string{"", pathA, pathB, "?domain_id=4294967295"} {
		response := request(nobody, query)
		if response.Code != http.StatusForbidden || response.Body.String() != "{\"error\":{\"code\":\"forbidden\",\"message\":\"Insufficient permissions\"}}\n" {
			t.Fatalf("unprivileged refusal changed: %d %s", response.Code, response.Body)
		}
	}
}
