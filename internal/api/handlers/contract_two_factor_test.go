package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/restmail/restmail"
	yaml "go.yaml.in/yaml/v2"
)

type twoFactorSchema struct {
	Ref                  string                     `yaml:"$ref"`
	Type                 string                     `yaml:"type"`
	Required             []string                   `yaml:"required"`
	Properties           map[string]twoFactorSchema `yaml:"properties"`
	Items                *twoFactorSchema           `yaml:"items"`
	AdditionalProperties *bool                      `yaml:"additionalProperties"`
}

func TestTwoFactor_ContractResponseFieldsMatchHandlers(t *testing.T) {
	served, err := restmail.OpenAPI.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Components struct {
			Schemas map[string]any `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(served, &doc); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		value any
	}{
		{"TwoFactorStatus", twoFactorStatusResponse{}},
		{"TwoFactorEnrollment", enrollResponse{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, ok := doc.Components.Schemas[tc.name]
			if !ok {
				t.Fatalf("missing response schema %s", tc.name)
			}
			encoded, err := yaml.Marshal(raw)
			if err != nil {
				t.Fatal(err)
			}
			var schema twoFactorSchema
			if err := yaml.Unmarshal(encoded, &schema); err != nil {
				t.Fatal(err)
			}
			if schema.Type != "object" || schema.AdditionalProperties == nil || *schema.AdditionalProperties {
				t.Fatal("response schema must enumerate its complete object fields")
			}
			typ := reflect.TypeOf(tc.value)
			var want, got []string
			for i := 0; i < typ.NumField(); i++ {
				field := typ.Field(i)
				name := strings.Split(field.Tag.Get("json"), ",")[0]
				want = append(want, name)
				property, ok := schema.Properties[name]
				if !ok {
					t.Errorf("handler field %s has no schema", name)
					continue
				}
				wantType := map[reflect.Kind]string{reflect.Bool: "boolean", reflect.String: "string", reflect.Slice: "array"}[field.Type.Kind()]
				if property.Type != wantType {
					t.Errorf("%s type %s, want %s", name, property.Type, wantType)
				}
				if field.Type.Kind() == reflect.Slice && (property.Items == nil || property.Items.Type != "string") {
					t.Errorf("%s must enumerate string recovery codes", name)
				}
			}
			for name := range schema.Properties {
				got = append(got, name)
			}
			required := append([]string(nil), schema.Required...)
			sort.Strings(want)
			sort.Strings(got)
			sort.Strings(required)
			if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(required, want) {
				t.Errorf("properties %v, required %v; handler fields %v", got, required, want)
			}
		})
	}
}

func TestTwoFactor_StatusIsOwnStateWithoutSecrets(t *testing.T) {
	store := newFakeStore()
	handler := newTFHandler(store)
	owner := mailboxClaims(1, "owner@restmail.test")
	read := func(enabled, pending bool) {
		t.Helper()
		rr := httptest.NewRecorder()
		twoFactorRoutes(handler).ServeHTTP(rr, withClaims(httptest.NewRequest(http.MethodGet, "/api/v1/auth/2fa", nil), owner))
		if rr.Code != http.StatusOK {
			t.Fatalf("status: %d %s", rr.Code, rr.Body)
		}
		var body struct {
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		want := map[string]any{"enabled": enabled, "pending": pending}
		if !reflect.DeepEqual(body.Data, want) {
			t.Errorf("status fields %v, want exactly %v", body.Data, want)
		}
	}
	read(false, false)
	doEnroll(t, handler, owner)
	read(false, true)
	credential, err := store.Get(owner.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Confirm(credential.ID); err != nil {
		t.Fatal(err)
	}
	read(true, false)
	// An account does not read another account's enrollment.
	owner = mailboxClaims(2, "other@restmail.test")
	read(false, false)
}
