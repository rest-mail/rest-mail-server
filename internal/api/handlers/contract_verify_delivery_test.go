package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/restmail/restmail"
	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/db/models"
	yaml "go.yaml.in/yaml/v2"
)

// verifyDelivery's to is any email address (#388): one with no mailbox has had
// nothing delivered to it, so the spec documents no 404 for it.
func TestVerifyDelivery_SpecHasNoNotFound(t *testing.T) {
	served, err := restmail.OpenAPI.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths map[string]struct {
			Get struct {
				OperationID string                 `yaml:"operationId"`
				Responses   map[string]interface{} `yaml:"responses"`
			} `yaml:"get"`
		} `yaml:"paths"`
	}
	if err := yaml.Unmarshal(served, &doc); err != nil {
		t.Fatal(err)
	}
	op := doc.Paths["/api/v1/admin/test/verify"].Get
	if op.OperationID != "verifyDelivery" {
		t.Fatalf("GET /api/v1/admin/test/verify is %q, want verifyDelivery", op.OperationID)
	}
	if _, ok := op.Responses["404"]; ok {
		t.Error("verifyDelivery documents a 404 the handler no longer answers")
	}
}

// The probe vertrag sends (a well-formed address no mailbox holds) is answered
// found: false at once, without polling out the timeout; a hosted mailbox is
// still searched.
func TestVerifyDelivery_Recipients(t *testing.T) {
	db, err := connectTestDB()
	if err != nil {
		t.Skipf("verifyDelivery requires PostgreSQL: %v", err)
	}
	if err := db.AutoMigrate(&models.Domain{}, &models.Mailbox{}, &models.Message{}); err != nil {
		t.Fatal(err)
	}
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })

	domain := models.Domain{Name: fmt.Sprintf("verify-%d.test", time.Now().UnixNano())}
	if err := tx.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	mb := models.Mailbox{DomainID: domain.ID, LocalPart: "eve", Address: "eve@" + domain.Name, Active: true}
	if err := tx.Create(&mb).Error; err != nil {
		t.Fatal(err)
	}
	msg := models.Message{MailboxID: mb.ID, Folder: "INBOX", Sender: "someone@example.com", Subject: "Delivered"}
	if err := tx.Create(&msg).Error; err != nil {
		t.Fatal(err)
	}

	h := NewTestHandler(tx, &config.Config{Environment: "test"})
	verify := func(query url.Values) map[string]interface{} {
		t.Helper()
		rr := httptest.NewRecorder()
		h.VerifyDelivery(rr, httptest.NewRequest(http.MethodGet, "/api/v1/admin/test/verify?"+query.Encode(), nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("%s: want 200, got %d: %s", query.Encode(), rr.Code, rr.Body)
		}
		var body struct {
			Data map[string]interface{} `json:"data"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body.Data
	}

	start := time.Now()
	if got := verify(url.Values{"to": {"someone@example.com"}, "timeout": {"30s"}}); got["found"] != false {
		t.Errorf("an address with no mailbox: want found false, got %v", got)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("an address with no mailbox polled for %s", elapsed)
	}
	if got := verify(url.Values{"to": {mb.Address}, "subject": {"Delivered"}}); got["found"] != true {
		t.Errorf("a delivered message: want found true, got %v", got)
	}
	if got := verify(url.Values{"to": {mb.Address}, "subject": {"Elsewhere"}, "timeout": {"0s"}}); got["found"] != false {
		t.Errorf("no matching message: want found false, got %v", got)
	}
}
