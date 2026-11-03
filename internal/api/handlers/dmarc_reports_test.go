package handlers

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/api/middleware"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/dmarcreceive"
	"github.com/rest-mail/rest-mail-server/internal/grants"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
	"github.com/rest-mail/rest-mail-server/internal/pipeline/filters"
	"gorm.io/gorm"
)

func dmarcRoutes(h *DMARCReportHandler) http.Handler {
	router := chi.NewRouter()
	h.Register(endpoint.Routes{Router: router, Registry: endpoint.NewRegistry()}, func(string) endpoint.Middleware {
		return endpoint.Middleware{Wrap: func(next http.Handler) http.Handler { return next }}
	})
	return router
}

func dmarcFixtureXML() string {
	return `<feedback><report_metadata><org_name>Reporter</org_name><email>reports@reporter.test</email><report_id>daily-1</report_id><date_range><begin>1793577600</begin><end>1793663999</end></date_range></report_metadata><policy_published><domain>hosted.test</domain><p>none</p></policy_published><record><row><source_ip>192.0.2.1</source_ip><count>12</count><policy_evaluated><disposition>none</disposition><dkim>pass</dkim><spf>fail</spf></policy_evaluated></row><identifiers><header_from>hosted.test</header_from></identifiers><auth_results><dkim><domain>hosted.test</domain><selector>mail</selector><result>pass</result></dkim></auth_results></record></feedback>`
}

type dmarcFixtureAuth struct{ result, domain string }

func (dmarcFixtureAuth) Name() string              { return "dmarc_check" }
func (dmarcFixtureAuth) Type() pipeline.FilterType { return pipeline.FilterTypeAction }
func (f dmarcFixtureAuth) Execute(ctx context.Context, _ *pipeline.EmailJSON) (*pipeline.FilterResult, error) {
	pipeline.AuthResultsFromContext(ctx).Add(pipeline.AuthResult{Method: pipeline.AuthDMARC, Result: f.result, Property: pipeline.PropHeaderFrom, Domain: f.domain})
	return &pipeline.FilterResult{Type: f.Type(), Action: pipeline.ActionContinue}, nil
}

func TestDMARCReportsMailDelivery(t *testing.T) {
	db := openAliasUpdateScratchDB(t)
	if err := db.AutoMigrate(append(accountModels, &models.Message{}, &models.Attachment{}, &models.Pipeline{}, &models.SieveScript{}, &models.DMARCReportReceived{})...); err != nil {
		t.Fatal(err)
	}
	domain := models.Domain{Name: "hosted.test", Active: true}
	if err := db.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	mailbox := models.Mailbox{DomainID: domain.ID, LocalPart: "postmaster", Address: "postmaster@hosted.test", Active: true, QuotaBytes: 1 << 30}
	if err := db.Create(&mailbox).Error; err != nil {
		t.Fatal(err)
	}
	seedReceiveFixture(t, db, mailbox)
	stored := models.Pipeline{DomainID: domain.ID, Direction: "inbound", Active: true, Filters: json.RawMessage(`[{"name":"dmarc_reports_received","enabled":false},{"name":"dmarc_check","type":"action","enabled":true}]`)}
	if err := db.Create(&stored).Error; err != nil {
		t.Fatal(err)
	}
	raw := dmarcFixtureXML()
	modern := strings.Replace(raw, "<feedback>", `<feedback xmlns="urn:ietf:params:xml:ns:dmarc-2.0">`, 1)
	modern = strings.Replace(modern, "<disposition>none</disposition>", "<disposition>pass</disposition>", 1)
	compress := func(media, raw string, entries int) []byte {
		var buffer bytes.Buffer
		switch media {
		case "application/gzip":
			writer := gzip.NewWriter(&buffer)
			if _, err := writer.Write([]byte(raw)); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
		case "application/zip":
			writer := zip.NewWriter(&buffer)
			for index := 0; index < entries; index++ {
				entry, err := writer.Create(fmt.Sprintf("report%d.xml", index))
				if err != nil {
					t.Fatal(err)
				}
				if _, err := entry.Write([]byte(raw)); err != nil {
					t.Fatal(err)
				}
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
		default:
			return []byte(raw)
		}
		return buffer.Bytes()
	}
	row := raw[strings.Index(raw, "<record>") : strings.Index(raw, "</record>")+len("</record>")]
	cases := []struct {
		name, media, raw, auth, authDomain string
		entries                            int
		stored                             bool
	}{
		{"XML 7489", "text/xml", raw, "pass", "reporter.test", 1, true},
		{"gzip 9990", "application/gzip", modern, "pass", "reporter.test", 1, true},
		{"zip 7489", "application/zip", raw, "pass", "reporter.test", 1, true},
		{"failed alignment", "text/xml", raw, "fail", "reporter.test", 1, false},
		{"another authenticated domain", "text/xml", raw, "pass", "forged.test", 1, false},
		{"forged header only", "text/xml", raw, "none", "reporter.test", 1, false},
		{"oversize XML", "text/xml", strings.Repeat("x", dmarcreceive.MaxBytes+1), "pass", "reporter.test", 1, false},
		{"gzip bomb", "application/gzip", strings.Repeat("x", dmarcreceive.MaxBytes+1), "pass", "reporter.test", 1, false},
		{"zip bomb", "application/zip", strings.Repeat("x", dmarcreceive.MaxBytes+1), "pass", "reporter.test", 1, false},
		{"zip entries", "application/zip", raw, "pass", "reporter.test", dmarcreceive.MaxZipEntries + 1, false},
		{"too many rows", "text/xml", strings.Replace(raw, row, strings.Repeat(row, dmarcreceive.MaxRows+1), 1), "pass", "reporter.test", 1, false},
		{"external entities", "text/xml", `<!DOCTYPE feedback [<!ENTITY xxe SYSTEM "http://127.0.0.1:1/private">]>` + raw, "pass", "reporter.test", 1, false},
		{"malformed XML", "text/xml", "<feedback>", "pass", "reporter.test", 1, false},
		{"other policy domain", "text/xml", strings.Replace(raw, "<domain>hosted.test", "<domain>other.test", 1), "pass", "reporter.test", 1, false},
		{"storage unavailable", "text/xml", raw, "pass", "reporter.test", 1, false},
	}
	for index, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if test.name == "storage unavailable" {
				if err := db.Callback().Create().Before("gorm:create").Register("dmarc_fixture_failure", func(tx *gorm.DB) {
					if _, ok := tx.Statement.Dest.(*models.DMARCReportReceived); ok {
						_ = tx.AddError(fmt.Errorf("private report storage failure"))
					}
				}); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = db.Callback().Create().Remove("dmarc_fixture_failure") })
			}
			var beforeReports, beforeMail int64
			db.Model(&models.DMARCReportReceived{}).Count(&beforeReports)
			db.Model(&models.Message{}).Count(&beforeMail)
			registry := withQuietVacation(pipeline.NewRegistry())
			registry.Register("dmarc_check", func([]byte) (pipeline.Filter, error) { return dmarcFixtureAuth{test.auth, test.authDomain}, nil })
			registry.Register("dmarc_reports_received", filters.NewDMARCReportsReceived)
			h := NewMessageHandler(db, nil, pipeline.NewEngine(registry, nil), "", nil)
			xml := strings.Replace(test.raw, "daily-1", fmt.Sprintf("daily-%d", index), 1)
			message := "From: reports@reporter.test\r\nTo: postmaster@hosted.test\r\nAuthentication-Results: restmail; dmarc=pass header.from=reporter.test\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=reports\r\n\r\n--reports\r\nContent-Type: text/plain\r\n\r\nAggregate report\r\n--reports\r\nContent-Type: " + test.media + "\r\nContent-Disposition: attachment; filename=report.xml\r\nContent-Transfer-Encoding: base64\r\n\r\n" + base64.StdEncoding.EncodeToString(compress(test.media, xml, test.entries)) + "\r\n--reports--\r\n"
			for attempt := 0; attempt < 2; attempt++ {
				mail, err := h.deliverToLocal(context.Background(), localDeliveryParams{Mailbox: mailbox, Sender: "reports@reporter.test", RawMessage: message, Subject: test.name})
				if err != nil || mail == nil || mail.MailboxID != mailbox.ID {
					t.Fatalf("postmaster delivery: %+v, %v", mail, err)
				}
			}
			var afterReports, afterMail int64
			if err := db.Model(&models.DMARCReportReceived{}).Count(&afterReports).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Model(&models.Message{}).Count(&afterMail).Error; err != nil {
				t.Fatal(err)
			}
			want := beforeReports
			if test.stored {
				want++
			}
			if afterReports != want || afterMail != beforeMail+2 {
				t.Fatalf("reports %d want %d, delivered %d want %d", afterReports, want, afterMail, beforeMail+2)
			}
			if test.stored {
				var report models.DMARCReportReceived
				if err := db.Order("id DESC").First(&report).Error; err != nil {
					t.Fatal(err)
				}
				if report.Reporter != "reporter.test" || report.ReportingOrg != "Reporter" || report.DomainID != domain.ID || len(report.Records) != 1 || report.Records[0].Row.Count != 12 || report.PolicyPublished.Domain != domain.Name {
					t.Fatalf("parsed report did not survive storage: %+v", report)
				}
			}
		})
	}
}

func TestDMARCReportRoutes(t *testing.T) {
	for _, path := range []string{"?domain_id=no", "?domain_id=-1", "?limit=201", "?limit=no", "?offset=1.5", "?reporter=" + strings.Repeat("x", 256), "/0", "/not-an-id"} {
		rr := httptest.NewRecorder()
		dmarcRoutes(NewDMARCReportHandler(nil)).ServeHTTP(rr, httptest.NewRequest("GET", "/api/v1/admin/dmarc-reports"+path, nil))
		if rr.Code != 422 {
			t.Fatalf("%s: %d %s", path, rr.Code, rr.Body)
		}
	}
	db := openAliasUpdateScratchDB(t)
	if err := db.AutoMigrate(&models.DMARCReportReceived{}); err != nil {
		t.Fatal(err)
	}
	domains := []models.Domain{{Name: "one.test"}, {Name: "two.test"}}
	if err := db.Create(&domains).Error; err != nil {
		t.Fatal(err)
	}
	parsed, err := dmarcreceive.Parse("text/xml", []byte(dmarcFixtureXML()))
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 3; index++ {
		report := parsed[0]
		report.DomainID = domains[index/2].ID
		report.Reporter = "reporter.test"
		report.ReportID = fmt.Sprintf("daily-%d", index)
		report.ReceivedAt = time.Now()
		if err := db.Create(&report).Error; err != nil {
			t.Fatal(err)
		}
	}
	router := dmarcRoutes(NewDMARCReportHandler(db))
	for _, test := range []struct {
		scope, path         string
		status, rows, total int
		more                bool
	}{
		{grants.Domain("one.test"), "?limit=1", 200, 1, 2, true},
		{grants.Domain("one.test"), "?limit=1&offset=1", 200, 1, 2, false},
		{grants.Domain("one.test"), fmt.Sprintf("?domain_id=%d", domains[1].ID), 200, 0, 0, false},
		{grants.Domain("one.test"), "?domain_id=4294967295", 200, 0, 0, false},
		{grants.Domain("one.test"), "?reporter=REPORTER", 200, 2, 2, false},
		{grants.Domain("one.test"), "?reporter=%25", 200, 0, 0, false},
		{grants.All, "", 200, 3, 3, false},
		{grants.Domain("one.test"), "/3", 404, 0, 0, false},
		{grants.Domain("one.test"), "/4294967295", 404, 0, 0, false},
		{grants.Domain("one.test"), "/1", 200, 0, 0, false},
		{grants.All, "/3", 200, 0, 0, false},
		{grants.Mailbox("one@one.test"), "", 403, 0, 0, false},
		{"", "", 403, 0, 0, false},
	} {
		rr := scopedRequest([]string{test.scope}, middleware.CapDomainsRead, router.ServeHTTP, "/api/v1/admin/dmarc-reports"+test.path)
		if rr.Code != test.status {
			t.Fatalf("%s %s: %d %s", test.scope, test.path, rr.Code, rr.Body)
		}
		if test.status == 200 && !strings.HasPrefix(test.path, "/") {
			var out struct {
				Data       []DMARCReport `json:"data"`
				Pagination struct {
					Total   int
					HasMore bool `json:"has_more"`
				} `json:"pagination"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
			if len(out.Data) != test.rows || out.Pagination.Total != test.total || out.Pagination.HasMore != test.more {
				t.Fatalf("scope/pagination leak: %s", rr.Body)
			}
		}
	}
}
