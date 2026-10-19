package handlers

import (
	"encoding/json"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/restmail/restmail"
	"github.com/restmail/restmail/internal/db/models"
	yaml "go.yaml.in/yaml/v2"
)

func TestCreateBan_ContractSchema(t *testing.T) {
	served, err := restmail.OpenAPI.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]struct {
					MinLength int    `yaml:"minLength"`
					Pattern   string `yaml:"pattern"`
				} `yaml:"properties"`
			} `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(served, &doc); err != nil {
		t.Fatal(err)
	}
	properties := doc.Components.Schemas["CreateBanRequest"].Properties
	if got := properties["ip"].MinLength; got != 1 {
		t.Errorf("IP minLength = %d, want 1", got)
	}
	pattern := properties["duration"].Pattern
	if pattern == "" {
		t.Fatal("duration needs a pattern: an arbitrary string is not a Go duration")
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"0", "1h", "168h", "1h30m", "-1.5s", "+.5ms", "1.s", "1us", "1µs", "1μs"} {
		if !re.MatchString(value) {
			t.Errorf("pattern refuses valid duration %q", value)
		}
	}
	for _, value := range []string{"", "x", "7d", "vertrag does not match", "1h\n"} {
		if re.MatchString(value) {
			t.Errorf("pattern permits invalid duration %q", value)
		}
	}
}

func TestCreateBan_ContractProbes(t *testing.T) {
	db := openMsgDeleteTestDB(t)
	if err := db.AutoMigrate(&models.Ban{}); err != nil {
		t.Fatal(err)
	}
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	h := NewBanHandler(tx)
	probes := []struct {
		name, body string
		want       int
	}{
		{"required fields", `{"created_by":"x","ip":"x","protocol":"smtp"}`, 201},
		{"created_by missing", `{"ip":"x","protocol":"smtp"}`, 422},
		{"ip missing", `{"created_by":"x","protocol":"smtp"}`, 422},
		{"protocol missing", `{"created_by":"x","ip":"x"}`, 422},
		{"created_by empty", `{"created_by":"","ip":"x","protocol":"smtp"}`, 200},
		{"created_by wrong type", `{"created_by":12345,"ip":"x","protocol":"smtp"}`, 400},
		{"duration empty", `{"created_by":"x","duration":"","ip":"x","protocol":"smtp"}`, 422},
		{"duration shape", `{"created_by":"x","duration":"0","ip":"x","protocol":"smtp"}`, 200},
		{"duration invalid pattern", `{"created_by":"x","duration":"vertrag does not match","ip":"x","protocol":"smtp"}`, 422},
		{"duration wrong type", `{"created_by":"x","duration":12345,"ip":"x","protocol":"smtp"}`, 400},
		{"ip empty", `{"created_by":"x","ip":"","protocol":"smtp"}`, 422},
		{"ip wrong type", `{"created_by":"x","ip":12345,"protocol":"smtp"}`, 400},
		{"smtp", `{"created_by":"x","ip":"x","protocol":"smtp"}`, 200},
		{"imap", `{"created_by":"x","ip":"x","protocol":"imap"}`, 200},
		{"pop3", `{"created_by":"x","ip":"x","protocol":"pop3"}`, 200},
		{"all", `{"created_by":"x","ip":"x","protocol":"all"}`, 200},
		{"protocol invalid enum", `{"created_by":"x","ip":"x","protocol":"vertrag-not-in-enum"}`, 422},
		{"protocol empty", `{"created_by":"x","ip":"x","protocol":""}`, 422},
		{"reason empty", `{"created_by":"x","ip":"x","protocol":"smtp","reason":""}`, 200},
		{"reason wrong type", `{"created_by":"x","ip":"x","protocol":"smtp","reason":12345}`, 400},
	}
	for _, probe := range probes {
		t.Run(probe.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			h.CreateBan(response, httptest.NewRequest("POST", "/api/v1/admin/bans", strings.NewReader(probe.body)))
			if response.Code != probe.want {
				t.Fatalf("status %d, want %d: %s", response.Code, probe.want, response.Body)
			}
			if response.Code == 422 {
				var body struct {
					Error struct {
						Code string `json:"code"`
					} `json:"error"`
				}
				if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				if body.Error.Code != "validation_failed" {
					t.Errorf("error code = %q", body.Error.Code)
				}
			}
		})
	}
}
