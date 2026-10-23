package handlers

// Request bodies the contract tests (#388) found accepted against the spec:
// a non-string refresh_token on logout, an unknown service on the probe, and
// a vacation update without "enabled". It also pins the spec's rule that an
// enabled vacation needs a subject and a body, which the handler enforces.

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/db/models"
	yaml "go.yaml.in/yaml/v2"
)

func TestLogoutRejectsNonStringRefreshToken(t *testing.T) {
	jwt := auth.NewJWTService("native-secret", 15*time.Minute, 7*24*time.Hour)
	h := newRefreshHandler(jwt, newFakeRefreshStore())

	rr := httptest.NewRecorder()
	h.Logout(rr, httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", strings.NewReader(`{"refresh_token":12345}`)))
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 422, got %d (%s)", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"refresh_token"`) {
		t.Errorf("missing refresh_token field error: %s", rr.Body.String())
	}

	// No body, an empty object and an empty token still log out.
	for _, body := range []string{"", `{}`, `{"refresh_token":""}`} {
		rr := httptest.NewRecorder()
		h.Logout(rr, httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", strings.NewReader(body)))
		if rr.Code != http.StatusNoContent {
			t.Errorf("body %q: want 204, got %d (%s)", body, rr.Code, rr.Body.String())
		}
	}
}

func TestProbeServicesRejectsUnknownService(t *testing.T) {
	h := NewTestHandler(nil, &config.Config{Environment: "test"})
	rr := httptest.NewRecorder()
	h.ProbeServices(rr, httptest.NewRequest(http.MethodPost, "/api/v1/admin/test/probe",
		strings.NewReader(`{"services":["vertrag-not-in-enum"]}`)))
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 422, got %d (%s)", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"services"`) {
		t.Errorf("missing services field error: %s", rr.Body.String())
	}
}

func TestSetVacationRequiresEnabled(t *testing.T) {
	db := openAuthzTestDB(t)
	if err := db.AutoMigrate(&models.VacationConfig{}); err != nil {
		t.Skipf("vacation DB test skipped: migrate failed (%v)", err)
	}
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })
	fx := seedAuthzFixture(t, tx)
	h := NewVacationHandler(tx)

	put := func(body string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		serveVacation(h, rr, authzRequest(http.MethodPut, "/vacation", fx.ownerMailbox.ID,
			map[string]string{"id": uintToStr(fx.ownerMailbox.ID)}, body))
		return rr
	}

	for _, body := range []string{`{"subject":"x","body":"x"}`, `{"enabled":null,"subject":"x","body":"x"}`} {
		rr := put(body)
		if rr.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: want 422, got %d (%s)", body, rr.Code, rr.Body.String())
		}
		if !strings.Contains(rr.Body.String(), `"enabled":"required"`) {
			t.Errorf("%s: missing enabled field error: %s", body, rr.Body.String())
		}
	}

	for _, body := range []string{`{"enabled":false,"subject":"","body":""}`, `{"enabled":true,"subject":"x","body":"x"}`} {
		if rr := put(body); rr.Code != http.StatusOK {
			t.Errorf("%s: want 200, got %d (%s)", body, rr.Code, rr.Body.String())
		}
	}
}

// TestVacationConfig_ContractSchema pins how the spec states the enabled rule:
// anyOf with an enum, which draft-04 and 2020-12 validators read alike. Under
// if/then the contract tests' boundary probes still judged an empty subject
// valid beside enabled true, and counted the handler's 422 as a finding.
func TestVacationConfig_ContractSchema(t *testing.T) {
	served, err := restmail.OpenAPI.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	type branch struct {
		Properties map[string]struct {
			Enum      []interface{} `yaml:"enum"`
			MinLength int           `yaml:"minLength"`
		} `yaml:"properties"`
	}
	var doc struct {
		Components struct {
			Schemas map[string]struct {
				Required []string    `yaml:"required"`
				If       interface{} `yaml:"if"`
				Then     interface{} `yaml:"then"`
				AnyOf    []branch    `yaml:"anyOf"`
			} `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(served, &doc); err != nil {
		t.Fatal(err)
	}
	schema := doc.Components.Schemas["VacationConfig"]
	if schema.If != nil || schema.Then != nil {
		t.Error("VacationConfig still states the enabled rule with if/then")
	}
	if !reflect.DeepEqual(schema.Required, []string{"enabled", "subject", "body"}) {
		t.Errorf("VacationConfig required = %v, want [enabled subject body]", schema.Required)
	}
	if len(schema.AnyOf) != 2 {
		t.Fatalf("VacationConfig anyOf has %d branches, want 2", len(schema.AnyOf))
	}

	// permits reads the anyOf as a validator does: some branch whose every
	// stated property holds.
	permits := func(enabled bool, subject, body string) bool {
		values := map[string]interface{}{"enabled": enabled, "subject": subject, "body": body}
		for _, b := range schema.AnyOf {
			ok := true
			for name, rule := range b.Properties {
				value := values[name]
				if rule.Enum != nil {
					member := false
					for _, allowed := range rule.Enum {
						member = member || reflect.DeepEqual(allowed, value)
					}
					ok = ok && member
				}
				if s, isString := value.(string); isString && len(s) < rule.MinLength {
					ok = false
				}
			}
			if ok {
				return true
			}
		}
		return false
	}
	for _, tc := range []struct {
		enabled       bool
		subject, body string
		want          bool
	}{
		{true, "Away", "Back Monday", true},
		{true, "", "Back Monday", false},
		{true, "Away", "", false},
		{true, "", "", false},
		{false, "", "", true},
		{false, "Away", "Back Monday", true},
	} {
		if got := permits(tc.enabled, tc.subject, tc.body); got != tc.want {
			t.Errorf("enabled=%v subject=%q body=%q: schema permits = %v, want %v",
				tc.enabled, tc.subject, tc.body, got, tc.want)
		}
	}
}

func TestSetVacationEnabledNeedsSubjectAndBody(t *testing.T) {
	db := openAuthzTestDB(t)
	if err := db.AutoMigrate(&models.VacationConfig{}); err != nil {
		t.Skipf("vacation DB test skipped: migrate failed (%v)", err)
	}
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })
	fx := seedAuthzFixture(t, tx)
	h := NewVacationHandler(tx)

	put := func(body string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		serveVacation(h, rr, authzRequest(http.MethodPut, "/vacation", fx.ownerMailbox.ID,
			map[string]string{"id": uintToStr(fx.ownerMailbox.ID)}, body))
		return rr
	}

	for _, tc := range []struct {
		body   string
		fields []string
	}{
		{`{"enabled":true,"subject":"","body":"x"}`, []string{"subject"}},
		{`{"enabled":true,"subject":"x","body":""}`, []string{"body"}},
		{`{"enabled":true,"subject":"","body":""}`, []string{"subject", "body"}},
	} {
		rr := put(tc.body)
		if rr.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: want 422, got %d (%s)", tc.body, rr.Code, rr.Body.String())
			continue
		}
		for _, field := range tc.fields {
			if !strings.Contains(rr.Body.String(), `"`+field+`":"required when enabled"`) {
				t.Errorf("%s: missing %s field error: %s", tc.body, field, rr.Body.String())
			}
		}
	}

	var stored int64
	if err := tx.Model(&models.VacationConfig{}).Where("mailbox_id = ?", fx.ownerMailbox.ID).Count(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored != 0 {
		t.Errorf("refused updates stored %d vacation configs, want 0", stored)
	}
}
