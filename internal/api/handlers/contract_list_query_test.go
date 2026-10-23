package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/restmail/restmail"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/pipeline/filters"
	yaml "go.yaml.in/yaml/v2"
)

type listQuerySchema struct {
	Type    string   `yaml:"type"`
	Format  string   `yaml:"format"`
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
		"listAliases":       aliasRoutes(NewAliasHandler(tx)).ServeHTTP,
		"listMailboxes":     mailboxRoutes(NewMailboxHandler(tx, nil, nil)).ServeHTTP,
		"listPipelines":     pipeline.ListPipelines,
		"listCustomFilters": pipeline.ListCustomFilters,
		"listPipelineLogs":  pipeline.ListPipelineLogs,
		"listMessages":      NewMessageHandler(tx, nil, nil, "", nil).ListMessages,
		"listGrants": func(w http.ResponseWriter, r *http.Request) {
			r.URL.Path = "/api/v1/admin/grants"
			grantRoutes(NewGrantHandler(tx, nil, "")).ServeHTTP(w, r)
		},
		"listQueue": func(w http.ResponseWriter, r *http.Request) {
			r.URL.Path = "/api/v1/admin/queue"
			queueRoutes(NewQueueHandler(tx)).ServeHTTP(w, r)
		},
		"listBans": banRoutes(NewBanHandler(tx)).ServeHTTP,
		"listTlsReports": func(w http.ResponseWriter, r *http.Request) {
			r.URL.Path = "/api/v1/admin/tls-reports"
			r.URL.RawPath = ""
			policyRoutes(NewTLSReportHandler(tx)).ServeHTTP(w, r)
		},
		"listAuditEntries": auditRoutes(NewAuditHandler(tx)).ServeHTTP,
		"searchMessages":   NewSearchHandler(tx).Search,
		"getDeliveryLogs":  logRoutes(NewLogHandler(tx)).ServeHTTP,
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
							}
						}
						query := url.Values{param.Name: []string{value}}
						path := "/"
						switch op {
						case "listBans":
							path = "/api/v1/admin/bans"
						case "getDeliveryLogs":
							path = "/api/v1/admin/logs/delivery"
						case "listAuditEntries":
							path = "/api/v1/admin/audit"
						}
						req := authzRequest(http.MethodGet, path+"?"+query.Encode(), fx.mailbox.ID, map[string]string{"id": uintToStr(fx.mailbox.ID), "folder": "INBOX"}, "")
						rr := httptest.NewRecorder()
						if op == "listAliases" {
							req.URL.Path = "/api/v1/admin/aliases"
						}
						if op == "listMailboxes" {
							req.URL.Path = "/api/v1/admin/mailboxes"
						}
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

// Replay the enum, boolean and date-time probes against real handlers and
// PostgreSQL: a value the schema allows is answered, any other is refused with
// a field-specific 422 instead of being dropped or passed to SQL.
func TestListQueryTypedContractProbes(t *testing.T) {
	db, err := connectTestDB()
	if err != nil {
		t.Skipf("contract list DB tests require PostgreSQL: %v", err)
	}
	modelsToMigrate := append([]interface{}{}, accountModels...)
	modelsToMigrate = append(modelsToMigrate, &models.Message{}, &models.OutboundQueue{}, &models.Contact{})
	if err := db.AutoMigrate(modelsToMigrate...); err != nil {
		t.Fatal(err)
	}
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	fx := seedMsgDeleteFixture(t, tx, 0)
	handlers := map[string]http.HandlerFunc{
		"listContacts":    contactRoutes(NewContactHandler(tx)).ServeHTTP,
		"searchMessages":  NewSearchHandler(tx).Search,
		"getDeliveryLogs": logRoutes(NewLogHandler(tx)).ServeHTTP,
	}
	ops := listQueryOperations(t)
	for op, handler := range handlers {
		t.Run(op, func(t *testing.T) {
			count := 0
			for _, param := range ops[op].Parameters {
				var valid, invalid []string
				switch {
				case param.In != "query":
					continue
				case len(param.Schema.Enum) > 0:
					valid, invalid = param.Schema.Enum, []string{"vertrag-outside-enum", strings.ToUpper(param.Schema.Enum[0])}
				case param.Schema.Type == "boolean":
					valid, invalid = []string{"true", "false"}, []string{"vertrag-not-a-boolean", "1", "yes"}
				case param.Schema.Format == "date-time":
					valid, invalid = []string{"2026-10-21T09:00:00Z", "2026-10-21T10:00:00+01:00"}, []string{"vertrag-not-a-date", "2026-10-21", "2026-10-21 09:00:00"}
				default:
					continue
				}
				count++
				for _, value := range append(valid, invalid...) {
					want := http.StatusOK
					if slices.Contains(invalid, value) {
						want = http.StatusUnprocessableEntity
					}
					t.Run(param.Name+"="+value, func(t *testing.T) {
						query := url.Values{param.Name: []string{value}}
						path := "/"
						switch op {
						case "listBans":
							path = "/api/v1/admin/bans"
						case "getDeliveryLogs":
							path = "/api/v1/admin/logs/delivery"
						case "listAuditEntries":
							path = "/api/v1/admin/audit"
						case "listContacts":
							path = contactsPathOf(fx.mailbox.ID, "")
						}
						req := authzRequest(http.MethodGet, path+"?"+query.Encode(), fx.mailbox.ID, map[string]string{"id": uintToStr(fx.mailbox.ID)}, "")
						rr := httptest.NewRecorder()
						handler(rr, req)
						if rr.Code != want {
							t.Fatalf("status = %d, want %d; body %s", rr.Code, want, rr.Body.String())
						}
						if want == http.StatusOK {
							return
						}
						if _, ok := ops[op].Responses["422"]; !ok {
							t.Fatal("refusal 422 is not documented")
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
						if body.Error.Code != "validation_failed" || body.Error.Details.Fields[param.Name] == "" {
							t.Fatalf("error = %+v", body.Error)
						}
					})
				}
			}
			if count == 0 {
				t.Fatal("no typed query probes ran")
			}
		})
	}
}
