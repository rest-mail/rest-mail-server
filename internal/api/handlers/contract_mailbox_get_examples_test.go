package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail"
	"github.com/restmail/restmail/internal/db/models"
	yaml "go.yaml.in/yaml/v2"
)

// getMailbox has no request body: the missing parameter example and the hook
// select the 404, while createMailbox's link supplies the successful lookup.
func TestMailboxGet_DocumentedExamples(t *testing.T) {
	served, err := restmail.OpenAPI.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths map[string]struct {
			Get struct {
				OperationID string `yaml:"operationId"`
				Parameters  []struct {
					Name     string `yaml:"name"`
					In       string `yaml:"in"`
					Required bool   `yaml:"required"`
					Examples map[string]struct {
						Value any `yaml:"value"`
					} `yaml:"examples"`
				} `yaml:"parameters"`
				Responses map[string]struct {
					Content map[string]namedExamples `yaml:"content"`
				} `yaml:"responses"`
			} `yaml:"get"`
		} `yaml:"paths"`
	}
	if err := yaml.Unmarshal(served, &doc); err != nil {
		t.Fatal(err)
	}
	const path = "/api/v1/admin/mailboxes/{id}"
	op := doc.Paths[path].Get
	if op.OperationID != "getMailbox" || len(op.Parameters) != 1 {
		t.Fatalf("unexpected getMailbox operation or parameters: %+v", op)
	}
	parameter := op.Parameters[0]
	if parameter.Name != "id" || parameter.In != "path" || !parameter.Required || len(parameter.Examples) != 2 {
		t.Fatalf("unexpected ID parameter: %+v", parameter)
	}
	missing := contractPathExampleID(t, parameter.Examples["missing"].Value)
	if missing != 2147483647 || contractPathExampleID(t, parameter.Examples["found"].Value) != 1 {
		t.Fatalf("unexpected path examples: %+v", parameter.Examples)
	}
	examples := op.Responses["404"].Content["application/json"].Examples
	if len(examples) != 1 {
		t.Fatalf("404 examples: %+v", examples)
	}
	want := jsonValue(t, examples["missing"].Value)
	if !reflect.DeepEqual(want, parseShapeJSON(t, `{"error":{"code":"mailbox_not_found","message":"Mailbox not found"}}`)) {
		t.Fatalf("404 documents %s", mustJSON(want))
	}

	t.Run("PostgreSQL", func(t *testing.T) {
		gdb, err := connectTestDB()
		if err != nil {
			t.Skipf("mailbox lookup contract test needs PostgreSQL: %v", err)
		}
		if err := gdb.AutoMigrate(&models.Domain{}, &models.Mailbox{}, &models.QuotaUsage{}); err != nil {
			t.Fatal(err)
		}
		tx := gdb.Begin()
		if tx.Error != nil {
			t.Fatal(tx.Error)
		}
		t.Cleanup(func() { tx.Rollback() })
		domain := models.Domain{Name: "contract-mailbox-get.test", ServerType: "restmail"}
		if err := tx.Create(&domain).Error; err != nil {
			t.Fatal(err)
		}
		mailbox := models.Mailbox{DomainID: domain.ID, LocalPart: "sales", Address: "sales@" + domain.Name, Active: true}
		if err := tx.Create(&mailbox).Error; err != nil {
			t.Fatal(err)
		}
		// Reload PostgreSQL's stored timestamp precision and the association.
		if err := tx.Preload("Domain").Preload("QuotaUsage").First(&mailbox, mailbox.ID).Error; err != nil {
			t.Fatal(err)
		}
		var count int64
		if err := tx.Model(&models.Mailbox{}).Where("id = ?", missing).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("missing ID must not exist: count %d, error %v", count, err)
		}
		lookup := func(id uint) *httptest.ResponseRecorder {
			r := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/admin/mailboxes/%d", id), nil)
			ctx := chi.NewRouteContext()
			ctx.URLParams.Add("id", fmt.Sprint(id))
			r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, ctx))
			rr := httptest.NewRecorder()
			mailboxRoutes(NewMailboxHandler(tx, nil, nil)).ServeHTTP(rr, r)
			return rr
		}
		rr := lookup(missing)
		if rr.Code != http.StatusNotFound || !reflect.DeepEqual(decodeShapeBody(t, rr), want) {
			t.Fatalf("missing ID answered %d %s, want 404 %s", rr.Code, rr.Body, mustJSON(want))
		}
		rr = lookup(mailbox.ID)
		if rr.Code != http.StatusOK {
			t.Fatalf("created ID answered %d %s", rr.Code, rr.Body)
		}
		wantSuccess := map[string]interface{}{"data": parseShapeJSON(t, string(mustJSON(mailbox)))}
		if got := decodeShapeBody(t, rr); !reflect.DeepEqual(got, wantSuccess) {
			t.Fatalf("lookup returned %s, want %s", mustJSON(got), mustJSON(wantSuccess))
		}
		var stored models.Mailbox
		if err := tx.Preload("Domain").Preload("QuotaUsage").First(&stored, mailbox.ID).Error; err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(stored, mailbox) {
			t.Fatalf("lookup changed stored mailbox")
		}
		if err := tx.Model(&models.Mailbox{}).Where("id = ?", missing).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("lookup created missing ID: count %d, error %v", count, err)
		}
	})
}
