package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/db/repositories"
	"github.com/rest-mail/rest-mail-server/internal/examplepairs"
)

func TestDomainRetirementExamples(test *testing.T) {
	useRealGrants(test)
	fixture := newGrantsFixture(test)
	stamp := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	fixture.db.NowFunc = func() time.Time { return stamp }
	owner, viewer, noMFA := fixture.token(test, "owner"), fixture.token(test, "viewer"), fixture.token(test, "root")
	pairs, err := examplepairs.Read()
	if err != nil {
		test.Fatal(err)
	}
	for _, scenario := range []struct {
		name   string
		status int
	}{{"restored", 200}, {"unauthenticated", 401}, {"read_only", 403}, {"two_factor_required", 403}, {"missing", 404}, {"name_reused", 409}, {"invalid_id", 422}, {"restore_failed", 500}, {"request_too_large", 413}, {"rate_limited", 429}, {"session_unavailable", 503}} {
		test.Run(scenario.name, func(test *testing.T) {
			domain := models.Domain{ID: 65001, Name: "retired.test", ServerType: "restmail"}
			if err := fixture.db.Create(&domain).Error; err != nil {
				test.Fatal(err)
			}
			if _, err := repositories.CreateDomainPostmaster(fixture.db, domain); err != nil {
				test.Fatal(err)
			}
			if err := fixture.db.Create(&models.Pipeline{DomainID: domain.ID, Direction: "inbound", Filters: json.RawMessage(`[]`)}).Error; err != nil {
				test.Fatal(err)
			}
			path := fmt.Sprintf("/api/v1/admin/domains/%d", domain.ID)
			response := doRequest(fixture.router, http.MethodDelete, path, owner, "")
			if response.Code != 204 {
				test.Fatalf("retire: %d %s", response.Code, response.Body)
			}
			token := owner
			router, body := fixture.router, ""
			id := domain.ID
			switch scenario.name {
			case "unauthenticated":
				token = ""
			case "read_only":
				token = viewer
			case "two_factor_required":
				token = noMFA
			case "missing":
				id = 65002
			case "invalid_id":
				id = 0
			case "name_reused":
				if err := fixture.db.Create(&models.Domain{ID: 65002, Name: domain.Name}).Error; err != nil {
					test.Fatal(err)
				}
			case "restore_failed":
				if err := fixture.db.Exec(`CREATE FUNCTION refuse_retirement_restore() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'restore fixture failure'; END; $$`).Error; err != nil {
					test.Fatal(err)
				}
				if err := fixture.db.Exec(`CREATE TRIGGER refuse_retirement_restore BEFORE INSERT ON pipelines FOR EACH ROW EXECUTE FUNCTION refuse_retirement_restore()`).Error; err != nil {
					test.Fatal(err)
				}
				defer fixture.db.Exec(`DROP FUNCTION refuse_retirement_restore() CASCADE`)
				defer fixture.db.Exec(`DROP TRIGGER refuse_retirement_restore ON pipelines`)
			case "request_too_large", "rate_limited", "session_unavailable":
				middleware := installationSenderRuleMutationScenarios(test)[scenario.name]
				router, token = middleware.router, middleware.token
				if scenario.name == "request_too_large" {
					body = `{"padding":"` + strings.Repeat("x", 32) + `"}`
				}
			}
			response = doRequest(router, http.MethodPost, fmt.Sprintf("/api/v1/admin/domains/%d/restore", id), token, body)
			if response.Code != scenario.status {
				test.Fatalf("restore: %d want%d %s", response.Code, scenario.status, response.Body)
			}
			want, ok := pairs["restoreDomain"][scenario.name]
			if !ok {
				test.Fatal("named restoration pair missing")
			}
			examplepairs.Proven(test, "restoreDomain", scenario.name)
			if want.Response.Status != response.Code || want.Response.MediaType != response.Header().Get("Content-Type") {
				test.Fatal("named restoration status or media type differs")
			}
			actual := response.Body.Bytes()
			if scenario.status == http.StatusOK {
				var answer map[string]any
				if err := json.Unmarshal(actual, &answer); err != nil {
					test.Fatal(err)
				}
				data, ok := answer["data"].(map[string]any)
				if !ok {
					test.Fatalf("restored domain is not an object: %s", actual)
				}
				for _, field := range []string{"created_at", "updated_at"} {
					text, _ := data[field].(string)
					at, err := time.Parse(time.RFC3339Nano, text)
					if err != nil || !at.Equal(stamp) {
						test.Fatalf("restored %s %q, want exactly %s: %v", field, text, stamp.Format(time.RFC3339Nano), err)
					}
					data[field] = at.UTC().Format(time.RFC3339Nano)
				}
				actual, err = json.Marshal(answer)
				if err != nil {
					test.Fatal(err)
				}
			}
			assertPublicExampleBody(test, want.Response.Body, actual)
			var count int64
			if err := fixture.db.Model(&models.DomainRetirement{}).Where("domain_id = ?", domain.ID).Count(&count).Error; err != nil {
				test.Fatal(err)
			}
			if scenario.status == 200 {
				if count != 0 {
					test.Fatal("successful restore retained tombstone")
				}
				var restored models.Domain
				if err := fixture.db.First(&restored, domain.ID).Error; err != nil || restored.Name != domain.Name {
					test.Fatalf("restoration lost domain: %+v %v", restored, err)
				}
			} else if count != 1 {
				test.Fatal("failed restore lost recoverable state")
			}
			if scenario.status != 200 {
				for _, table := range []string{"domains", "mailboxes", "pipelines"} {
					column := "domain_id"
					if table == "domains" {
						column = "id"
					}
					if err := fixture.db.Table(table).Where(column+" = ?", domain.ID).Count(&count).Error; err != nil || count != 0 {
						test.Fatalf("failed restore left live %s: %d %v", table, count, err)
					}
				}
			}
			if err := fixture.db.Exec("DELETE FROM grants WHERE scope IN (?, ?)", "domain:"+domain.Name, "mailbox:postmaster@"+domain.Name).Error; err != nil {
				test.Fatal(err)
			}
			if err := fixture.db.Exec("DELETE FROM quota_usage WHERE mailbox_id IN (SELECT id FROM mailboxes WHERE domain_id = ?)", domain.ID).Error; err != nil {
				test.Fatal(err)
			}
			for _, table := range []string{"mailboxes", "pipelines", "domains"} {
				if err := fixture.db.Exec("DELETE FROM "+table+" WHERE "+map[bool]string{true: "id", false: "domain_id"}[table == "domains"]+" IN (?,?)", domain.ID, 65002).Error; err != nil {
					test.Fatal(err)
				}
			}
			if err := fixture.db.Delete(&models.DomainRetirement{}, "domain_id = ?", domain.ID).Error; err != nil {
				test.Fatal(err)
			}
		})
	}
}
