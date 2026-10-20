package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	"github.com/restmail/restmail"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/pipeline/filters"
	yaml "go.yaml.in/yaml/v2"
)

type listQuerySchema struct {
	Type    string   `yaml:"type"`
	Enum    []string `yaml:"enum"`
	Minimum *int64   `yaml:"minimum"`
	Maximum *int64   `yaml:"maximum"`
}

type listQueryOperation struct {
	ID         string                 `yaml:"operationId"`
	Responses  map[string]interface{} `yaml:"responses"`
	Parameters []struct {
		Name   string          `yaml:"name"`
		In     string          `yaml:"in"`
		Schema listQuerySchema `yaml:"schema"`
	} `yaml:"parameters"`
}

func listQueryOperations(t *testing.T) map[string]listQueryOperation {
	t.Helper()
	served, err := restmail.OpenAPI.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths map[string]struct {
			Get listQueryOperation `yaml:"get"`
		} `yaml:"paths"`
	}
	if err := yaml.Unmarshal(served, &doc); err != nil {
		t.Fatal(err)
	}
	ops := map[string]listQueryOperation{}
	for _, path := range doc.Paths {
		ops[path.Get.ID] = path.Get
	}
	return ops
}

// These IDs already use unsigned parsing. Their schemas must not tell the
// runner that negative values are valid requests.
func TestListQueryUnsignedSchemas(t *testing.T) {
	ops := listQueryOperations(t)
	for op, name := range map[string]string{"listAuditEntries": "actor_id", "listGrants": "holder_id", "listTlsReports": "domain_id"} {
		t.Run(op, func(t *testing.T) {
			for _, param := range ops[op].Parameters {
				if param.Name == name {
					if param.Schema.Minimum == nil || *param.Schema.Minimum != 0 {
						t.Fatalf("%s must document minimum: 0", name)
					}
					return
				}
			}
			t.Fatalf("missing parameter %s", name)
		})
	}
}

// Replay the integer type and boundary probes against real handlers and
// PostgreSQL. Valid negative/zero pagination values retain the existing default
// behavior when the schema has no minimum; malformed values must be refused.
func TestListQueryIntegerContractProbes(t *testing.T) {
	db, err := connectTestDB()
	if err != nil {
		t.Skipf("contract list DB tests require PostgreSQL: %v", err)
	}
	modelsToMigrate := append([]interface{}{}, accountModels...)
	modelsToMigrate = append(modelsToMigrate, &models.Message{}, &models.Alias{}, &models.Pipeline{}, &models.CustomFilter{}, &models.MessageTrace{}, &models.OutboundQueue{}, &models.Ban{}, &models.TLSReport{}, &models.AuditEntry{})
	if err := db.AutoMigrate(modelsToMigrate...); err != nil {
		t.Fatal(err)
	}
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	fx := seedMsgDeleteFixture(t, tx, 0)
	pipeline := NewPipelineHandler(tx, nil, filters.JSSidecar{})
	handlers := map[string]http.HandlerFunc{
		"listAliases":       NewAliasHandler(tx).List,
		"listMailboxes":     NewMailboxHandler(tx, nil, nil).List,
		"listPipelines":     pipeline.ListPipelines,
		"listCustomFilters": pipeline.ListCustomFilters,
		"listPipelineLogs":  pipeline.ListPipelineLogs,
		"listMessages":      NewMessageHandler(tx, nil, nil, "", nil).ListMessages,
		"listGrants":        NewGrantHandler(tx, nil, "").List,
		"listQueue":         NewQueueHandler(tx).ListQueue,
		"listBans":          NewBanHandler(tx).ListBans,
		"listTlsReports":    NewTLSReportHandler(tx).ListReports,
		"listAuditEntries":  NewAuditHandler(tx).List,
	}
	ops := listQueryOperations(t)
	for op, handler := range handlers {
		t.Run(op, func(t *testing.T) {
			count := 0
			for _, param := range ops[op].Parameters {
				if param.In != "query" || param.Schema.Type != "integer" {
					continue
				}
				count++
				values := []string{"", "-1", "0", "1", "vertrag-not-a-number", "1.5"}
				if param.Schema.Maximum != nil {
					values = append(values, strconv.FormatInt(*param.Schema.Maximum, 10), strconv.FormatInt(*param.Schema.Maximum+1, 10))
				} else {
					values = append(values, "201")
				}
				for _, value := range values {
					t.Run(param.Name+"="+value, func(t *testing.T) {
						want := http.StatusOK
						if value != "" {
							n, err := strconv.ParseInt(value, 10, 64)
							if err != nil || (param.Schema.Minimum != nil && n < *param.Schema.Minimum) || (param.Schema.Maximum != nil && n > *param.Schema.Maximum) {
								want = http.StatusUnprocessableEntity
								if op == "listTlsReports" && param.Name == "domain_id" {
									want = http.StatusBadRequest
								}
							}
						}
						query := url.Values{param.Name: []string{value}}
						req := authzRequest(http.MethodGet, "/?"+query.Encode(), fx.mailbox.ID, map[string]string{"id": uintToStr(fx.mailbox.ID), "folder": "INBOX"}, "")
						rr := httptest.NewRecorder()
						handler(rr, req)
						if rr.Code != want {
							t.Fatalf("status = %d, want %d; body %s", rr.Code, want, rr.Body.String())
						}
						if want != http.StatusOK {
							if _, ok := ops[op].Responses[strconv.Itoa(want)]; !ok {
								t.Fatalf("refusal %d is not documented", want)
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
							if want == http.StatusUnprocessableEntity && (body.Error.Code != "validation_failed" || body.Error.Details.Fields[param.Name] == "") {
								t.Fatalf("error = %+v", body.Error)
							}
						}
					})
				}
			}
			if count == 0 {
				t.Fatal("no integer query probes ran")
			}
		})
	}
}
