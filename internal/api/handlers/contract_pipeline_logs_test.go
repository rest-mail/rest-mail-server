package handlers

import (
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/pipeline/filters"
)

func TestListPipelineLogs_RejectInvalidEnums(t *testing.T) {
	for name, valid := range map[string]string{"direction": "inbound", "action": "continue", "outcome": "delivered"} {
		for _, value := range []string{"", "vertrag-not-in-enum", strings.ToUpper(valid), valid + " ", "12345"} {
			t.Run(name+"="+value, func(t *testing.T) {
				rr := httptest.NewRecorder()
				query := url.Values{name: {value}}
				// Invalid filters must be refused before database access.
				pipelineRoutes(NewPipelineHandler(nil, nil, filters.JSSidecar{})).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/admin/pipelines/logs?"+query.Encode(), nil))
				if rr.Code != http.StatusUnprocessableEntity {
					t.Fatalf("status = %d, want 422: %s", rr.Code, rr.Body)
				}
				var body struct {
					Error struct {
						Code    string `json:"code"`
						Message string `json:"message"`
						Details struct {
							Fields map[string]string `json:"fields"`
						} `json:"details"`
					} `json:"error"`
				}
				if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				if body.Error.Code != "validation_failed" || body.Error.Details.Fields[name] == "" {
					t.Fatalf("missing %s validation error: %s", name, rr.Body)
				}
				// The refused field is a query parameter, and the message says so (#786).
				if body.Error.Message != "Query parameters failed validation" {
					t.Fatalf("message = %q, want the query parameters named", body.Error.Message)
				}
			})
		}
	}
}

func TestListPipelineLogs_ContractEnumFilters(t *testing.T) {
	db, err := connectTestDB()
	if err != nil {
		t.Skipf("pipeline logs DB test requires PostgreSQL: %v", err)
	}
	if err := db.AutoMigrate(&models.MessageTrace{}); err != nil {
		t.Fatal(err)
	}
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	// Other packages' tests commit traces concurrently; block their writes
	// until rollback so the counts below see only this test's rows.
	if err := tx.Exec("LOCK TABLE message_traces IN EXCLUSIVE MODE").Error; err != nil {
		t.Fatal(err)
	}
	if err := tx.Where("1 = 1").Delete(&models.MessageTrace{}).Error; err != nil {
		t.Fatal(err)
	}

	op := listQueryOperations(t)["listPipelineLogs"]
	enums := map[string][]string{
		"direction": {"inbound", "outbound"},
		"action":    {"continue", "reject", "quarantine", "discard", "defer"},
		"outcome":   {"delivered", "queued", "rejected", "quarantined", "discarded", "deferred"},
	}
	for name, want := range enums {
		var got []string
		for _, param := range op.Parameters {
			if param.Name == name {
				got = param.Schema.Enum
			}
		}
		if !slices.Equal(got, want) {
			t.Fatalf("%s schema = %v, want %v", name, got, want)
		}
	}
	if _, ok := op.Responses["422"]; !ok {
		t.Fatal("enum validation response must be documented")
	}

	now := time.Now().UTC().Truncate(time.Second)
	actions := []string{"continue", "continue", "reject", "quarantine", "discard", "defer"}
	var traces []models.MessageTrace
	for _, direction := range enums["direction"] {
		for i, outcome := range enums["outcome"] {
			for j := range 2 {
				traces = append(traces, models.MessageTrace{
					PipelineID: uint(j + 1), Direction: direction,
					FinalAction: actions[i], Outcome: outcome,
					ReasonCode: "contract-reason", RFCMessageID: "<contract@restmail.test>",
					Stages: json.RawMessage(`[]`), CreatedAt: now.Add(time.Duration(len(traces)) * time.Minute),
				})
			}
		}
	}
	if err := tx.Create(&traces).Error; err != nil {
		t.Fatal(err)
	}
	queries := []url.Values{
		{},
		{
			"direction": {"outbound"}, "action": {"reject"}, "outcome": {"rejected"},
			"pipeline_id": {"2"}, "reason_code": {"contract-reason"},
			"rfc_message_id": {"<contract@restmail.test>"},
		},
		{"direction": {"inbound"}, "outcome": {"queued"}, "action": {"reject"}},
	}
	for name, values := range enums {
		for _, value := range values {
			queries = append(queries, url.Values{name: {value}})
		}
	}
	for _, query := range queries {
		for _, paginated := range []bool{false, true} {
			query := maps.Clone(query)
			limit, offset := 50, 0
			if paginated {
				limit, offset = 1, 1
				query.Set("limit", "1")
				query.Set("offset", "1")
			}
			t.Run(query.Encode(), func(t *testing.T) {
				rr := httptest.NewRecorder()
				pipelineRoutes(NewPipelineHandler(tx, nil, filters.JSSidecar{})).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/admin/pipelines/logs?"+query.Encode(), nil))
				if rr.Code != http.StatusOK {
					t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body)
				}
				var body struct {
					Data       []models.MessageTrace `json:"data"`
					Pagination struct {
						Total   int  `json:"total"`
						HasMore bool `json:"has_more"`
					} `json:"pagination"`
				}
				if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				var want []uint
				for i := len(traces) - 1; i >= 0; i-- {
					trace := traces[i]
					values := map[string]string{
						"direction": trace.Direction, "action": trace.FinalAction,
						"outcome": trace.Outcome, "pipeline_id": uintToStr(trace.PipelineID),
						"reason_code": trace.ReasonCode, "rfc_message_id": trace.RFCMessageID,
					}
					match := true
					for name, value := range values {
						if query.Has(name) && query.Get(name) != value {
							match = false
						}
					}
					if match {
						want = append(want, trace.ID)
					}
				}
				if body.Pagination.Total != len(want) || body.Pagination.HasMore != (offset+limit < len(want)) {
					t.Fatalf("pagination = %+v, want total %d, has_more %v", body.Pagination, len(want), offset+limit < len(want))
				}
				want = want[min(offset, len(want)):min(offset+limit, len(want))]
				var got []uint
				for _, trace := range body.Data {
					got = append(got, trace.ID)
				}
				if !slices.Equal(got, want) {
					t.Fatalf("IDs = %v, want %v", got, want)
				}
			})
		}
	}
}
