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

// Invalid filters must be refused before querying the database. A nil DB
// makes a regression that reaches SQL fail immediately.
func TestListBans_RejectInvalidFilters(t *testing.T) {
	for field, values := range map[string][]string{
		"protocol": {"", "vertrag-not-in-enum", "SMTP", "smtp ", "12345"},
		"active":   {"", "vertrag-not-a-boolean", "1", "0", "TRUE", "False", "true "},
	} {
		for _, value := range values {
			t.Run(field+"="+value, func(t *testing.T) {
				query := url.Values{field: {value}}
				rr := httptest.NewRecorder()
				NewBanHandler(nil).ListBans(rr, httptest.NewRequest(http.MethodGet, "/api/v1/admin/bans?"+query.Encode(), nil))
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
				if body.Error.Code != "validation_failed" || body.Error.Details.Fields[field] == "" {
					t.Fatalf("missing validation error for %s: %s", field, rr.Body)
				}
			})
		}
	}
}

// Exercise each documented enum and boolean value against real bans, including
// an expired ban and a protocol=all ban that applies to every protocol.
func TestListBans_ContractFilters(t *testing.T) {
	db := openMsgDeleteTestDB(t)
	if err := db.AutoMigrate(&models.Ban{}); err != nil {
		t.Fatal(err)
	}
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	if err := tx.Where("1 = 1").Delete(&models.Ban{}).Error; err != nil {
		t.Fatal(err)
	}
	expired := time.Now().Add(-time.Hour)
	future := time.Now().Add(time.Hour)
	bans := []models.Ban{
		{IP: "192.0.2.1", Protocol: "smtp"},
		{IP: "192.0.2.2", Protocol: "imap", ExpiresAt: &future},
		{IP: "192.0.2.3", Protocol: "pop3"},
		{IP: "192.0.2.4", Protocol: "all"},
		{IP: "192.0.2.5", Protocol: "smtp", ExpiresAt: &expired},
	}
	if err := tx.Create(&bans).Error; err != nil {
		t.Fatal(err)
	}
	ops := listQueryOperations(t)
	var protocols []string
	for _, param := range ops["listBans"].Parameters {
		if param.Name == "protocol" {
			protocols = param.Schema.Enum
		}
	}
	if !slices.Equal(protocols, []string{"smtp", "imap", "pop3", "all"}) {
		t.Fatalf("unexpected protocol schema: %v", protocols)
	}
	if _, ok := ops["listBans"].Responses["422"]; !ok {
		t.Fatal("filter validation response must be documented")
	}
	for _, protocol := range append([]string{""}, protocols...) {
		for _, active := range []string{"", "true", "false"} {
			t.Run(protocol+"/active="+active, func(t *testing.T) {
				query := url.Values{}
				if protocol != "" {
					query.Set("protocol", protocol)
				}
				if active != "" {
					query.Set("active", active)
				}
				rr := httptest.NewRecorder()
				NewBanHandler(tx).ListBans(rr, httptest.NewRequest(http.MethodGet, "/api/v1/admin/bans?"+query.Encode(), nil))
				if rr.Code != http.StatusOK {
					t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body)
				}
				var body struct {
					Data       []models.Ban `json:"data"`
					Pagination struct {
						Total int `json:"total"`
					} `json:"pagination"`
				}
				if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				var want, got []string
				for _, ban := range bans {
					if protocol != "" && ban.Protocol != protocol && ban.Protocol != "all" {
						continue
					}
					if active != "false" && ban.ExpiresAt != nil && ban.ExpiresAt.Before(time.Now()) {
						continue
					}
					want = append(want, ban.IP)
				}
				for _, ban := range body.Data {
					got = append(got, ban.IP)
				}
				slices.Sort(got)
				slices.Sort(want)
				if !slices.Equal(got, want) || body.Pagination.Total != len(want) {
					t.Fatalf("IPs = %v, total = %d; want %v", got, body.Pagination.Total, want)
				}
			})
		}
	}
}
