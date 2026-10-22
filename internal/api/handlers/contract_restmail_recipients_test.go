package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/restmail/restmail"
	"github.com/restmail/restmail/internal/db/models"
	yaml "go.yaml.in/yaml/v2"
)

func TestRestmailDeliver_RecipientSchema(t *testing.T) {
	served, err := restmail.OpenAPI.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	type property struct {
		Type      interface{} `yaml:"type"`
		MinItems  int         `yaml:"minItems"`
		MinLength int         `yaml:"minLength"`
	}
	type rule struct {
		Required   []string            `yaml:"required"`
		Properties map[string]property `yaml:"properties"`
	}
	var doc struct {
		Components struct {
			Schemas map[string]struct {
				Required   []string            `yaml:"required"`
				Properties map[string]property `yaml:"properties"`
				AnyOf      []rule              `yaml:"anyOf"`
			} `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(served, &doc); err != nil {
		t.Fatal(err)
	}
	schema := doc.Components.Schemas["RestmailDeliverRequest"]
	if len(schema.Required) != 0 {
		t.Errorf("unconditionally required = %v; recipient sources are alternatives", schema.Required)
	}
	want := []rule{
		{Required: []string{"to"}, Properties: map[string]property{"to": {Type: "array", MinItems: 1}}},
		{Required: []string{"recipients"}, Properties: map[string]property{"recipients": {Type: "array", MinItems: 1}}},
		{Required: []string{"raw_message"}, Properties: map[string]property{"raw_message": {Type: "string", MinLength: 1}}},
	}
	if !reflect.DeepEqual(schema.AnyOf, want) {
		t.Errorf("recipient alternatives = %+v, want %+v", schema.AnyOf, want)
	}
	for _, field := range []string{"to", "recipients"} {
		p := schema.Properties[field]
		if !reflect.DeepEqual(p.Type, []interface{}{"array", "null"}) || p.MinItems != 0 {
			t.Errorf("%s = %+v; empty/null lists must allow another source to supply recipients", field, p)
		}
	}
}

func TestRestmailDeliver_EmptyRecipientSources(t *testing.T) {
	// These fail before any domain lookup, authentication or storage.
	h := NewRestmailHandler(nil, nil, nil, RestmailTarpitConfig{}, RestmailDeliverAuthConfig{})
	for _, body := range []string{
		`{}`, `{"to":[]}`, `{"recipients":[]}`, `{"to":null,"recipients":null}`,
		`{"to":[],"recipients":[],"raw_message":""}`,
		`{"raw_message":"From: sender@external.test\r\nSubject: No recipients\r\n\r\nhello"}`,
	} {
		t.Run(body, func(t *testing.T) {
			rr := httptest.NewRecorder()
			h.Deliver(rr, httptest.NewRequest(http.MethodPost, restmailMessagesPath, bytes.NewBufferString(body)))
			want := parseShapeJSON(t, `{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"to":"at least one recipient required"}}}}`)
			if rr.Code != http.StatusUnprocessableEntity || !reflect.DeepEqual(parseShapeJSON(t, rr.Body.String()), want) {
				t.Fatalf("got %d %s, want recipient validation 422", rr.Code, rr.Body)
			}
		})
	}
}

func TestRestmailDeliver_RecipientSources(t *testing.T) {
	db := openPlusAddrTestDB(t)
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	domain := models.Domain{Name: "recipient-contract.test", Active: true}
	if err := tx.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	mailbox := models.Mailbox{DomainID: domain.ID, LocalPart: "eve", Address: "eve@recipient-contract.test", Active: true}
	if err := tx.Create(&mailbox).Error; err != nil {
		t.Fatal(err)
	}
	h := NewRestmailHandler(tx, nil, nil, RestmailTarpitConfig{}, RestmailDeliverAuthConfig{TrustedCIDRs: []string{"203.0.113.0/24"}})
	raw := "From: sender@external.test\r\nTo: " + mailbox.Address + "\r\nDate: Sat, 17 Oct 2026 12:00:00 +0000\r\nSubject: Recipient source\r\n\r\nhello"
	for _, tc := range []struct {
		name   string
		fields map[string]interface{}
	}{
		{"to", map[string]interface{}{"to": []string{mailbox.Address}}},
		{"recipients", map[string]interface{}{"recipients": []string{mailbox.Address}}},
		{"empty_to", map[string]interface{}{"to": []string{}, "recipients": []string{mailbox.Address}}},
		{"null_to", map[string]interface{}{"to": nil, "recipients": []string{mailbox.Address}}},
		{"raw", map[string]interface{}{"raw_message": raw}},
		{"empty_lists_raw", map[string]interface{}{"to": []string{}, "recipients": []string{}, "raw_message": raw}},
		{"null_lists_raw", map[string]interface{}{"to": nil, "recipients": nil, "raw_message": raw}},
		{"to_precedes_alias", map[string]interface{}{"to": []string{mailbox.Address}, "recipients": []string{"missing@recipient-contract.test"}}},
		{"alias_precedes_raw", map[string]interface{}{"recipients": []string{mailbox.Address}, "raw_message": "To: missing@recipient-contract.test\r\n\r\nhello"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := map[string]interface{}{"from": "sender@external.test", "date": "Sat, 17 Oct 2026 12:00:00 +0000", "subject": "Recipient source"}
			for k, v := range tc.fields {
				body[k] = v
			}
			encoded, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			rr := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, restmailMessagesPath, bytes.NewReader(encoded))
			req.RemoteAddr = "203.0.113.9:5000"
			h.Deliver(rr, req)
			want := map[string]interface{}{"data": map[string]interface{}{"delivered": []interface{}{mailbox.Address}, "failed": []interface{}{}}}
			if rr.Code != http.StatusCreated || !reflect.DeepEqual(parseShapeJSON(t, rr.Body.String()), want) {
				t.Fatalf("got %d %s, want delivery to %s", rr.Code, rr.Body, mailbox.Address)
			}
		})
	}
	var stored int64
	if err := tx.Model(&models.Message{}).Where("mailbox_id = ?", mailbox.ID).Count(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored != 9 {
		t.Errorf("stored %d messages, want one for each recipient source case (9)", stored)
	}
}
