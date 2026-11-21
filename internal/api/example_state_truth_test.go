package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/api/handlers"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/grants"
)

func TestSuccessfulExamplesRequireTheirSetupState(t *testing.T) {
	fixture := newGrantsFixture(t)
	owner := fixture.token(t, "owner")
	pairs := readPublishedDescriptionPairs(t)
	doc, _ := liveDescription(t, fixture.router)
	compiler := descriptionSchemaCompiler(t, doc)
	t.Run("listTlsReports/available", func(t *testing.T) {
		const path = "/api/v1/admin/tls-reports"
		list := publishedDescriptionPair(t, pairs, "listTlsReports", "available")
		stored := publishedDescriptionPair(t, pairs, "receiveTlsReport", "stored")
		domain, _ := seedDomain(t, fixture.db, "restmail.test")
		read := func() endpoint.List[handlers.TLSReport] {
			response := sendDescriptionPair(fixture.router, requirementRoute{http.MethodGet, path}, owner, list)
			assertStateExampleWire(t, list.Response.Status, list.Response.MediaType, response)
			assertDescribedJSON(t, compiler, http.MethodGet, path, response)
			var page endpoint.List[handlers.TLSReport]
			decodeInto(t, response, &page)
			return page
		}
		without := read()
		const intake = "/.well-known/smtp-tlsrpt"
		response := sendDescriptionPair(fixture.router, requirementRoute{http.MethodPost, intake}, "", stored)
		assertRequirementResponse(t, stored, response)
		assertDescribedJSON(t, compiler, http.MethodPost, intake, response)
		var persisted models.TLSReport
		if err := fixture.db.Where("domain_id = ? AND reporting_org = ?", domain.ID, "Example Reporter").Take(&persisted).Error; err != nil {
			t.Fatalf("intake did not persist the report: %v", err)
		}
		assertPublicExampleBody(t, stored.Request.Body, []byte(persisted.RawReport))
		assertSetupChangesExample(t, read(), without, func(row handlers.TLSReport) bool {
			return row.ID == persisted.ID && row.DomainID == domain.ID && row.PolicyDomain == domain.Name &&
				row.ReportingOrg == "Example Reporter" && row.PolicyType == "sts" && row.TotalSuccessful == 12 &&
				row.TotalFailure == 1 && row.RawReport == persisted.RawReport
		})
	})
	t.Run("listCustomFilters/available", func(t *testing.T) {
		const path = "/api/v1/admin/custom-filters"
		list := publishedDescriptionPair(t, pairs, "listCustomFilters", "available")
		create := publishedDescriptionPair(t, pairs, "createCustomFilter", "enabled_default")
		read := func() endpoint.List[handlers.CustomFilter] {
			response := sendDescriptionPair(fixture.router, requirementRoute{http.MethodGet, path}, owner, list)
			assertStateExampleWire(t, list.Response.Status, list.Response.MediaType, response)
			assertDescribedJSON(t, compiler, http.MethodGet, path, response)
			var page endpoint.List[handlers.CustomFilter]
			decodeInto(t, response, &page)
			return page
		}
		without := read()
		response := sendDescriptionPair(fixture.router, requirementRoute{http.MethodPost, path}, owner, create)
		assertStateExampleWire(t, create.Response.Status, create.Response.MediaType, response)
		assertDescribedJSON(t, compiler, http.MethodPost, path, response)
		var created endpoint.Data[handlers.CustomFilter]
		decodeInto(t, response, &created)
		if created.Data.ID == 0 {
			t.Fatalf("creation did not identify the filter: %s", response.Body)
		}
		var persisted models.CustomFilter
		if err := fixture.db.First(&persisted, created.Data.ID).Error; err != nil {
			t.Fatalf("create did not persist filter %d: %v", created.Data.ID, err)
		}
		const script = `{"script":"function filter(email) {\n  return { action: \"continue\" };\n}\n"}`
		assertPublicExampleBody(t, []byte(script), persisted.Config)
		assertSetupChangesExample(t, read(), without, func(row handlers.CustomFilter) bool {
			var listedConfig, storedConfig struct{ Script string }
			if json.Unmarshal(row.Config, &listedConfig) != nil || json.Unmarshal(persisted.Config, &storedConfig) != nil {
				return false
			}
			return row.ID == persisted.ID && row.DomainID == fixture.a.ID && row.Name == "tag-invoices" &&
				row.Description == "Tags mail about invoices" && row.Enabled && row.FilterType == "action" && row.Direction == "inbound" && listedConfig == storedConfig
		})
	})
	for _, lookup := range []struct{ operation, path string }{
		{"checkMailboxAddress", "/api/mailboxes"}, {"restmailCheckMailbox", "/restmail/mailboxes"},
	} {
		t.Run(lookup.operation+"/found", func(t *testing.T) {
			pair := publishedDescriptionPair(t, pairs, lookup.operation, "found")
			address, ok := pair.Request.Query["address"].(string)
			local, host, split := strings.Cut(address, "@")
			if !ok || !split || local == "" || host == "" {
				t.Fatalf("found has no address: %v", pair.Request.Query)
			}
			domain, _ := seedDomain(t, fixture.db, host)
			mailbox := models.Mailbox{DomainID: domain.ID, Address: address, LocalPart: local, Active: true}
			read := func() handlers.AddressCheck {
				response := sendDescriptionPair(fixture.router, requirementRoute{http.MethodGet, lookup.path}, "", pair)
				assertStateExampleWire(t, pair.Response.Status, pair.Response.MediaType, response)
				assertDescribedJSON(t, compiler, http.MethodGet, lookup.path, response)
				var result endpoint.Data[handlers.AddressCheck]
				decodeInto(t, response, &result)
				return result.Data
			}
			without := read()
			absentAddress := ""
			exposesID := lookup.operation == "checkMailboxAddress"
			if exposesID {
				absentAddress = address
			}
			if without.Exists || without.Address != absentAddress || without.MailboxID != 0 {
				t.Fatalf("without mailbox: %+v, want address %q with exists:false and no id", without, absentAddress)
			}
			if err := fixture.db.Create(&mailbox).Error; err != nil {
				t.Fatal(err)
			}
			if err := grants.EnsureReceive(fixture.db, &mailbox); err != nil {
				t.Fatal(err)
			}
			expectedID := uint(0)
			if exposesID {
				expectedID = mailbox.ID
			}
			requireFound := func(result handlers.AddressCheck) error {
				if !result.Exists || result.Address != mailbox.Address || result.MailboxID != expectedID {
					return fmt.Errorf("got %+v, want %s with exists:true and mailbox_id=%d", result, mailbox.Address, expectedID)
				}
				return nil
			}
			if err := requireFound(read()); err != nil {
				t.Fatal(err)
			}
			if err := requireFound(without); err == nil {
				t.Fatal("accepted the found example without creating its mailbox")
			} else {
				t.Logf("no-setup control rejected: %v", err)
			}
		})
	}
}

func assertStateExampleWire(t *testing.T, status int, media string, response *httptest.ResponseRecorder) {
	t.Helper()
	if response.Code != status || (media != "" && response.Header().Get("Content-Type") != media) {
		t.Fatalf("got %d %s %s, want %d %s", response.Code, response.Header().Get("Content-Type"), response.Body, status, media)
	}
}

func assertSetupChangesExample[T any](t *testing.T, with, without endpoint.List[T], matches func(T) bool) {
	t.Helper()
	if without.Data == nil || len(without.Data) != 0 || without.Pagination == nil || without.Pagination.HasMore || without.Pagination.Total != 0 {
		t.Fatalf("without setup, want a non-null empty page: %+v", without)
	}
	requireRow := func(page endpoint.List[T]) error {
		if len(page.Data) != 1 {
			return fmt.Errorf("listed %d rows, want exactly the prepared row", len(page.Data))
		}
		if page.Pagination == nil || page.Pagination.HasMore || page.Pagination.Total != 1 || !matches(page.Data[0]) {
			return fmt.Errorf("page or row does not match the prepared state: %+v", page.Data[0])
		}
		return nil
	}
	if err := requireRow(with); err != nil {
		t.Fatal(err)
	}
	if err := requireRow(without); err == nil {
		t.Fatal("accepted a listing success without its prepared row")
	} else {
		t.Logf("no-setup control rejected: %v", err)
	}
}
