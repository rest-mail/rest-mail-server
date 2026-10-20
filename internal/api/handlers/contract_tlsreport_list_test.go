package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/db/models"
)

func TestListTLSReports_RejectInvalidPolicyType(t *testing.T) {
	for _, policyType := range []string{"", "vertrag-not-in-enum", "STS", "sts ", "no-policy-found", "12345"} {
		t.Run("policy_type="+policyType, func(t *testing.T) {
			query := url.Values{"policy_type": {policyType}}
			rr := httptest.NewRecorder()
			// A nil DB proves invalid filters are refused before database access.
			NewTLSReportHandler(nil).ListReports(rr, httptest.NewRequest(http.MethodGet, "/api/v1/admin/tls-reports?"+query.Encode(), nil))
			if rr.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422: %s", rr.Code, rr.Body)
			}
			var body struct {
				Error struct {
					Code    string `json:"code"`
					Details struct {
						Fields map[string]string `json:"fields"`
					} `json:"details"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Error.Code != "validation_failed" || body.Error.Details.Fields["policy_type"] == "" {
				t.Fatalf("missing policy_type validation error: %s", rr.Body)
			}
		})
	}
}

func TestListTLSReports_ContractPolicyFilters(t *testing.T) {
	db, err := connectTestDB()
	if err != nil {
		t.Skipf("TLS report list DB test requires PostgreSQL: %v", err)
	}
	if err := db.AutoMigrate(&models.Domain{}, &models.TLSReport{}); err != nil {
		t.Fatal(err)
	}
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	if err := tx.Where("1 = 1").Delete(&models.TLSReport{}).Error; err != nil {
		t.Fatal(err)
	}
	domain := models.Domain{Name: "tlsreport-contract.test"}
	if err := tx.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}

	op := listQueryOperations(t)["listTlsReports"]
	var policyTypes []string
	for _, param := range op.Parameters {
		if param.Name == "policy_type" {
			policyTypes = param.Schema.Enum
		}
	}
	if !slices.Equal(policyTypes, []string{"sts", "tlsa", "no-policy"}) {
		t.Fatalf("unexpected policy_type schema: %v", policyTypes)
	}
	if _, ok := op.Responses["422"]; !ok {
		t.Fatal("policy_type validation response must be documented")
	}

	now := time.Now().UTC().Truncate(time.Second)
	var reports []models.TLSReport
	for i, policyType := range policyTypes {
		for j := range 2 {
			reports = append(reports, models.TLSReport{
				DomainID: domain.ID, ReportingOrg: "Reporter Inc", PolicyType: policyType,
				PolicyDomain: domain.Name, StartDate: now.Add(-24 * time.Hour), EndDate: now,
				ReceivedAt: now.Add(time.Duration(i*2+j) * time.Minute),
			})
		}
	}
	if err := tx.Create(&reports).Error; err != nil {
		t.Fatal(err)
	}
	for _, policyType := range append([]string{""}, policyTypes...) {
		for _, paginated := range []bool{false, true} {
			name := policyType
			if paginated {
				name += "/paginated"
			}
			t.Run(name, func(t *testing.T) {
				query := url.Values{}
				if policyType != "" {
					query.Set("policy_type", policyType)
				}
				limit, offset := 50, 0
				if paginated {
					limit, offset = 1, 1
					query.Set("limit", "1")
					query.Set("offset", "1")
				}
				rr := httptest.NewRecorder()
				NewTLSReportHandler(tx).ListReports(rr, httptest.NewRequest(http.MethodGet, "/api/v1/admin/tls-reports?"+query.Encode(), nil))
				if rr.Code != http.StatusOK {
					t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body)
				}
				var body struct {
					Data       []models.TLSReport `json:"data"`
					Pagination struct {
						Total   int  `json:"total"`
						HasMore bool `json:"has_more"`
					} `json:"pagination"`
				}
				if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				var want []uint
				for i := len(reports) - 1; i >= 0; i-- {
					if policyType == "" || reports[i].PolicyType == policyType {
						want = append(want, reports[i].ID)
					}
				}
				if body.Pagination.Total != len(want) || body.Pagination.HasMore != (offset+limit < len(want)) {
					t.Fatalf("pagination = %+v, want total %d, has_more %v", body.Pagination, len(want), offset+limit < len(want))
				}
				want = want[offset:min(offset+limit, len(want))]
				var got []uint
				for _, report := range body.Data {
					got = append(got, report.ID)
				}
				if !slices.Equal(got, want) {
					t.Fatalf("IDs = %v, want %v", got, want)
				}
			})
		}
	}
}
