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
		h.SetVacation(rr, authzRequest(http.MethodPut, "/vacation", fx.ownerMailbox.ID,
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

func TestVacationConfig_ContractSchema(t *testing.T) {
	served, err := restmail.OpenAPI.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	type rule struct {
		Required   []string `yaml:"required"`
		Properties map[string]struct {
			Const     *bool `yaml:"const"`
			MinLength int   `yaml:"minLength"`
		} `yaml:"properties"`
	}
	var doc struct {
		Components struct {
			Schemas map[string]struct {
				If   rule `yaml:"if"`
				Then rule `yaml:"then"`
			} `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(served, &doc); err != nil {
		t.Fatal(err)
	}
	schema := doc.Components.Schemas["VacationConfig"]
	if enabled := schema.If.Properties["enabled"].Const; enabled == nil || !*enabled {
		t.Errorf("VacationConfig if: enabled const = %v, want true", enabled)
	}
	if !reflect.DeepEqual(schema.If.Required, []string{"enabled"}) {
		t.Errorf("VacationConfig if: required = %v, want [enabled]", schema.If.Required)
	}
	for _, field := range []string{"subject", "body"} {
		if got := schema.Then.Properties[field].MinLength; got != 1 {
			t.Errorf("VacationConfig then: %s minLength = %d, want 1", field, got)
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
		h.SetVacation(rr, authzRequest(http.MethodPut, "/vacation", fx.ownerMailbox.ID,
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
