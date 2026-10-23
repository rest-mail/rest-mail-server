package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/restmail/restmail"
	"github.com/restmail/restmail/internal/db/models"
	yaml "go.yaml.in/yaml/v2"
)

// The create schema must describe the default the handler applies, while the
// response still promises only concrete server types (#388).
func TestDomainCreate_DefaultContract(t *testing.T) {
	served, err := restmail.OpenAPI.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Components struct {
			Schemas map[string]struct {
				Required   []string `yaml:"required"`
				Properties map[string]struct {
					Type      interface{}   `yaml:"type"`
					Enum      []interface{} `yaml:"enum"`
					Default   string        `yaml:"default"`
					MinLength int           `yaml:"minLength"`
				} `yaml:"properties"`
			} `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(served, &doc); err != nil {
		t.Fatal(err)
	}
	request := doc.Components.Schemas["CreateDomainRequest"]
	if !reflect.DeepEqual(request.Required, []string{"name"}) {
		t.Errorf("required = %v, want only name; server_type defaults when omitted", request.Required)
	}
	if request.Properties["name"].MinLength != 1 {
		t.Error("name must exclude the empty string the handler refuses")
	}
	serverType := request.Properties["server_type"]
	if !reflect.DeepEqual(serverType.Type, []interface{}{"string", "null"}) ||
		!reflect.DeepEqual(serverType.Enum, []interface{}{"traditional", "restmail", "", nil}) ||
		serverType.Default != "traditional" {
		t.Errorf("server_type = %+v, want traditional/restmail or empty/null with default traditional", serverType)
	}
	response := doc.Components.Schemas["Domain"].Properties["server_type"]
	if response.Type != "string" || !reflect.DeepEqual(response.Enum, []interface{}{"traditional", "restmail"}) {
		t.Errorf("response server_type = %+v, want only concrete string values", response)
	}
}

func TestDomainCreate_ServerTypeDefault(t *testing.T) {
	gdb := openAuthzTestDB(t)
	if err := gdb.AutoMigrate(&models.Pipeline{}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, field, want string
	}{
		{"omitted", "", "traditional"},
		{"empty", `,"server_type":""`, "traditional"},
		{"null", `,"server_type":null`, "traditional"},
		{"traditional", `,"server_type":"traditional"`, "traditional"},
		{"restmail", `,"server_type":"restmail"`, "restmail"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx := gdb.Begin()
			if tx.Error != nil {
				t.Fatal(tx.Error)
			}
			t.Cleanup(func() { tx.Rollback() })
			rr := httptest.NewRecorder()
			domainRoutes(NewDomainHandler(tx, nil)).ServeHTTP(rr, httptest.NewRequest(http.MethodPost,
				"/api/v1/admin/domains", strings.NewReader(`{"name":"default-contract.test"`+tc.field+`}`)))
			if rr.Code != http.StatusCreated {
				t.Fatalf("status = %d, want 201; body %s", rr.Code, rr.Body)
			}
			var response struct {
				Data models.Domain `json:"data"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			var stored models.Domain
			if err := tx.First(&stored, response.Data.ID).Error; err != nil {
				t.Fatal(err)
			}
			if stored.ServerType != tc.want || response.Data.ServerType != tc.want {
				t.Errorf("stored = %q, response = %q; want %q", stored.ServerType, response.Data.ServerType, tc.want)
			}
		})
	}
}

func TestDomainCreate_EmptyNameContract(t *testing.T) {
	rr := httptest.NewRecorder()
	domainRoutes(NewDomainHandler(nil, nil)).ServeHTTP(rr, httptest.NewRequest(http.MethodPost,
		"/api/v1/admin/domains", strings.NewReader(`{"name":"","server_type":"traditional"}`)))
	if rr.Code != http.StatusUnprocessableEntity || errorCodeOf(rr) != "validation_failed" {
		t.Fatalf("status = %d, body %s; want 422 validation_failed", rr.Code, rr.Body)
	}
}
