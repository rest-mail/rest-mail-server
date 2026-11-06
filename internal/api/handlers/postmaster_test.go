package handlers

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/smtp"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/api/middleware"
	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/bootstrap"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/gateway/apiclient"
	"github.com/rest-mail/rest-mail-server/internal/gateway/connlimiter"
	smtpgateway "github.com/rest-mail/rest-mail-server/internal/gateway/smtp"
	"github.com/rest-mail/rest-mail-server/internal/grants"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
	"github.com/rest-mail/rest-mail-server/internal/pipeline/filters"
	"gorm.io/gorm"
)

func openPostmasterDB(t *testing.T) *gorm.DB {
	t.Helper()
	database := openAliasUpdateScratchDB(t)
	if err := database.AutoMigrate(append(accountModels, &models.Pipeline{}, &models.Certificate{}, &models.Message{}, &models.Attachment{}, &models.SieveScript{}, &models.DMARCReportReceived{}, &models.OutboundQueue{})...); err != nil {
		t.Fatal(err)
	}
	seedReceiveFixture(t, database)
	if err := bootstrap.EnsureRBAC(database); err != nil {
		t.Fatal(err)
	}
	return database
}

func postmasterRequest(t *testing.T, handler http.Handler, claims *auth.Claims, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if claims != nil {
		request = request.WithContext(context.WithValue(request.Context(), middleware.ClaimsKey, claims))
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestPostmasterDomainCreationDefaults(t *testing.T) {
	for _, quota := range []int64{1 << 30, 0, 4096} {
		t.Run(fmt.Sprint(quota), func(t *testing.T) {
			database := openPostmasterDB(t)
			handler := domainRoutes(NewDomainHandler(database, nil))
			response := postmasterRequest(t, handler, nil, http.MethodPost, "/api/v1/admin/domains", fmt.Sprintf(`{"name":"new.test","default_quota_bytes":%d}`, quota))
			if response.Code != http.StatusCreated {
				t.Fatalf("create: %d %s", response.Code, response.Body)
			}
			var mailbox models.Mailbox
			if err := database.Where("address = ?", "postmaster@new.test").First(&mailbox).Error; err != nil {
				t.Fatalf("domain creation must provision postmaster: %v", err)
			}
			if !mailbox.Active || mailbox.QuotaBytes != quota || mailbox.OwnerID != nil || mailbox.DisplayName != "" {
				t.Fatalf("ordinary account defaults: %+v", mailbox)
			}
			allowed, err := grants.CanReceive(database, &mailbox)
			if err != nil || !allowed {
				t.Fatalf("receive grant: %v, %v", allowed, err)
			}
			var usage models.QuotaUsage
			if err := database.First(&usage, "mailbox_id = ?", mailbox.ID).Error; err != nil {
				t.Fatal(err)
			}
			var credentials int64
			if err := database.Model(&models.Credential{}).Where("account_id = ?", mailbox.ID).Count(&credentials).Error; err != nil || credentials != 0 {
				t.Fatalf("no generated password or credentials: %d, %v", credentials, err)
			}
		})
	}
}

func TestPostmasterProvisioningFailureRollsBack(t *testing.T) {
	database := openPostmasterDB(t)
	const callback = "postmaster_quota_failure"
	if err := database.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "quota_usage" {
			_ = tx.AddError(errors.New("postmaster provisioning failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Callback().Create().Remove(callback) })
	response := postmasterRequest(t, domainRoutes(NewDomainHandler(database, nil)), nil, http.MethodPost, "/api/v1/admin/domains", `{"name":"rollback.test"}`)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("provisioning failure: %d %s", response.Code, response.Body)
	}
	for _, model := range []any{&models.Domain{}, &models.Pipeline{}, &models.Mailbox{}, &models.QuotaUsage{}} {
		var count int64
		if err := database.Model(model).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("partial provisioning remains (%T): %d %v", model, count, err)
		}
	}
}

func TestPostmasterSMTPReportDelivery(t *testing.T) {
	for _, alias := range []bool{false, true} {
		t.Run(fmt.Sprintf("alias=%v", alias), func(t *testing.T) {
			database := openPostmasterDB(t)
			response := postmasterRequest(t, domainRoutes(NewDomainHandler(database, nil)), nil, http.MethodPost, "/api/v1/admin/domains", `{"name":"hosted.test"}`)
			if response.Code != http.StatusCreated {
				t.Fatalf("create: %d %s", response.Code, response.Body)
			}
			var domain models.Domain
			if err := database.Where("name = ?", "hosted.test").First(&domain).Error; err != nil {
				t.Fatal(err)
			}
			if err := database.Model(&domain).Update("active", true).Error; err != nil {
				t.Fatal(err)
			}
			var mailbox models.Mailbox
			if alias {
				mailbox = models.Mailbox{DomainID: domain.ID, LocalPart: "reports", Address: "reports@hosted.test", Active: true}
				if err := database.Create(&mailbox).Error; err != nil {
					t.Fatal(err)
				}
				seedReceiveFixture(t, database, mailbox)
				if err := database.Create(&models.Alias{DomainID: domain.ID, SourceAddress: "postmaster@hosted.test", DestinationAddress: mailbox.Address, Active: true}).Error; err != nil {
					t.Fatal(err)
				}
				if err := database.Where("address = ?", "postmaster@hosted.test").Delete(&models.Mailbox{}).Error; err != nil {
					t.Fatal(err)
				}
			} else {
				_ = database.Where("address = ?", "postmaster@hosted.test").First(&mailbox).Error
			}
			registry := withQuietVacation(pipeline.NewRegistry())
			registry.Register("dmarc_check", func([]byte) (pipeline.Filter, error) { return dmarcFixtureAuth{"pass", "reporter.test"}, nil })
			registry.Register("dmarc_reports_received", filters.NewDMARCReportsReceived)
			registry.Register("recipient_check", filters.NewRecipientCheck(database))
			configured := models.JSONB(`[{"name":"recipient_check","type":"action","enabled":true},{"name":"dmarc_check","type":"action","enabled":true}]`)
			if err := database.Model(&models.Pipeline{}).Where("domain_id = ? AND direction = ?", domain.ID, "inbound").Update("filters", configured).Error; err != nil {
				t.Fatal(err)
			}
			router := chi.NewRouter()
			routes := endpoint.Routes{Router: router, Registry: endpoint.NewRegistry()}
			NewMailboxHandler(database, nil, nil).RegisterAddressCheck(routes)
			NewMessageHandler(database, nil, pipeline.NewEngine(registry, nil), "", nil).RegisterDelivery(routes, addressTestPass(""))
			api := httptest.NewServer(router)
			t.Cleanup(api.Close)
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			server := smtpgateway.NewServer("mx.hosted.test", apiclient.New(api.URL), nil, smtpgateway.NewStore(database, ""), connlimiter.New(connlimiter.Config{}))
			if err := server.Serve(smtpgateway.SMTPListeners{Inbound: listener}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { server.Shutdown() })
			client, err := smtp.Dial(listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = client.Close() }()
			if err := client.Mail("reports@reporter.test"); err != nil {
				t.Fatal(err)
			}
			if err := client.Rcpt("postmaster@hosted.test"); err != nil {
				t.Fatalf("RCPT postmaster must be accepted: %v", err)
			}
			writer, err := client.Data()
			if err != nil {
				t.Fatal(err)
			}
			raw := "From: reports@reporter.test\r\nTo: postmaster@hosted.test\r\nSubject: aggregate report\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=reports\r\n\r\n--reports\r\nContent-Type: text/plain\r\n\r\nAggregate report\r\n--reports\r\nContent-Type: text/xml\r\nContent-Disposition: attachment; filename=report.xml\r\nContent-Transfer-Encoding: base64\r\n\r\n" + base64.StdEncoding.EncodeToString([]byte(dmarcFixtureXML())) + "\r\n--reports--\r\n"
			if _, err := writer.Write([]byte(raw)); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatalf("DATA: %v", err)
			}
			if err := client.Quit(); err != nil {
				t.Fatal(err)
			}
			var reports, messages int64
			if err := database.Model(&models.DMARCReportReceived{}).Where("domain_id = ?", domain.ID).Count(&reports).Error; err != nil {
				t.Fatal(err)
			}
			if err := database.Model(&models.Message{}).Where("mailbox_id = ?", mailbox.ID).Count(&messages).Error; err != nil {
				t.Fatal(err)
			}
			if reports != 1 || messages != 1 {
				t.Fatalf("parsed report AND ordinary mail delivery: reports=%d messages=%d", reports, messages)
			}
			var report models.DMARCReportReceived
			if err := database.First(&report).Error; err != nil || report.Reporter != "reporter.test" || report.ReportID != "daily-1" {
				t.Fatalf("stored report: %+v, %v", report, err)
			}
			var message models.Message
			if err := database.First(&message, "mailbox_id = ?", mailbox.ID).Error; err != nil || message.Subject != "aggregate report" {
				t.Fatalf("delivered message: %+v, %v", message, err)
			}
		})
	}
}

func TestPostmasterMissingAddressBlocksActivation(t *testing.T) {
	database := openPostmasterDB(t)
	domain := models.Domain{Name: "missing.test"}
	if err := database.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	response := postmasterRequest(t, domainRoutes(NewDomainHandler(database, nil)), nil, http.MethodPatch, fmt.Sprintf("/api/v1/admin/domains/%d", domain.ID), `{"active":true}`)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "postmaster_required") {
		t.Fatalf("missing receiving address must prevent activation: %d %s", response.Code, response.Body)
	}
}

func TestPostmasterPreservationAndAliasReplacement(t *testing.T) {
	database := openPostmasterDB(t)
	fixture := seedAccountsFixture(t, database)
	var superadmin models.Role
	if err := database.Where("name = ?", "superadmin").First(&superadmin).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := grants.EnsureSystem(database, fixture.admin.ID, superadmin.ID, "*", models.GrantActive); err != nil {
		t.Fatal(err)
	}
	claims := &auth.Claims{AccountID: fixture.admin.ID, AMR: []string{auth.AMRPassword, auth.AMROTP}, TwoFactor: true}
	cache := grants.NewCache(database, grants.CacheTTL)
	mailboxes := mailboxRoutes(NewMailboxHandler(database, cache, nil))
	aliases := aliasRoutes(NewAliasHandler(database))
	domains := domainRoutes(NewDomainHandler(database, nil, cache))
	grantHandler := grantRoutes(NewGrantHandler(database, cache, ""))
	response := postmasterRequest(t, domains, claims, http.MethodPost, "/api/v1/admin/domains", `{"name":"required.test"}`)
	if response.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", response.Code, response.Body)
	}
	var mailbox models.Mailbox
	if err := database.Where("address = ?", "postmaster@required.test").First(&mailbox).Error; err != nil {
		t.Fatal(err)
	}
	var owner models.Grant
	if err := database.Joins("JOIN roles ON roles.id = grants.role_id").Where("holder_id = ? AND scope = ? AND roles.name = ?", fixture.admin.ID, grants.Mailbox(mailbox.Address), models.RoleOwner).First(&owner).Error; err != nil {
		t.Fatalf("ordinary creator owner grant: %v", err)
	}
	refused := func(handler http.Handler, method, path, body string) {
		t.Helper()
		response := postmasterRequest(t, handler, claims, method, path, body)
		if response.Code != http.StatusConflict || errorCodeOf(response) != "postmaster_required" {
			t.Fatalf("last receiving address must remain: %s %s: %d %s", method, path, response.Code, response.Body)
		}
	}
	mailboxPath := fmt.Sprintf("/api/v1/admin/mailboxes/%d", mailbox.ID)
	refused(mailboxes, http.MethodDelete, mailboxPath, "")
	refused(mailboxes, http.MethodPatch, mailboxPath, `{"active":false}`)
	var receiver models.Grant
	if err := database.Where("holder_id = ? AND scope = ?", mailbox.ID, grants.Mailbox(mailbox.Address)).First(&receiver).Error; err != nil {
		t.Fatal(err)
	}
	refused(grantHandler, http.MethodDelete, fmt.Sprintf("/api/v1/admin/grants/%d", receiver.ID), "")
	target := models.Mailbox{DomainID: mailbox.DomainID, LocalPart: "reports", Address: "reports@required.test", Active: true}
	if err := database.Create(&target).Error; err != nil {
		t.Fatal(err)
	}
	if err := grants.EnsureReceive(database, &target); err != nil {
		t.Fatal(err)
	}
	response = postmasterRequest(t, aliases, claims, http.MethodPost, "/api/v1/admin/aliases", `{"source_address":"postmaster@required.test","destination_address":"reports@required.test"}`)
	if response.Code != http.StatusCreated {
		t.Fatalf("replacement alias: %d %s", response.Code, response.Body)
	}
	refused(mailboxes, http.MethodPatch, mailboxPath, `{"active":false}`)
	response = postmasterRequest(t, mailboxes, claims, http.MethodDelete, mailboxPath, "")
	if response.Code != http.StatusNoContent {
		t.Fatalf("alias permits account removal atomically: %d %s", response.Code, response.Body)
	}
	var alias models.Alias
	if err := database.Where("source_address = ?", mailbox.Address).First(&alias).Error; err != nil {
		t.Fatal(err)
	}
	aliasPath := fmt.Sprintf("/api/v1/admin/aliases/%d", alias.ID)
	refused(aliases, http.MethodDelete, aliasPath, "")
	refused(aliases, http.MethodPatch, aliasPath, `{"active":false}`)
	refused(aliases, http.MethodPatch, aliasPath, `{"destination_address":"missing@required.test"}`)
	refused(aliases, http.MethodPatch, aliasPath, `{"destination_address":"postmaster@required.test"}`)
	refused(mailboxes, http.MethodDelete, fmt.Sprintf("/api/v1/admin/mailboxes/%d", target.ID), "")
	refused(mailboxes, http.MethodPatch, fmt.Sprintf("/api/v1/admin/mailboxes/%d", target.ID), `{"active":false}`)
	receiver = models.Grant{}
	if err := database.Where("holder_id = ? AND scope = ?", target.ID, grants.Mailbox(target.Address)).First(&receiver).Error; err != nil {
		t.Fatal(err)
	}
	refused(grantHandler, http.MethodDelete, fmt.Sprintf("/api/v1/admin/grants/%d", receiver.ID), "")
	var domain models.Domain
	if err := database.First(&domain, mailbox.DomainID).Error; err != nil {
		t.Fatal(err)
	}
	if configured, err := postmasterConfigured(database, domain); err != nil || !configured {
		t.Fatalf("all refusals preserve receiving alias: %v %v", configured, err)
	}
	key, keyPEM := throwawayKey(t)
	certPEM := throwawayCertificate(t, key, domain.Name, []string{domain.Name}, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	if err := database.Create(&models.Certificate{DomainID: domain.ID, CertPEM: certPEM, KeyPEM: keyPEM, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}).Error; err != nil {
		t.Fatal(err)
	}
	response = postmasterRequest(t, domains, claims, http.MethodPatch, fmt.Sprintf("/api/v1/admin/domains/%d", domain.ID), `{"active":true}`)
	if response.Code != http.StatusOK {
		t.Fatalf("receiving alias allows activation: %d %s", response.Code, response.Body)
	}
	response = postmasterRequest(t, aliases, claims, http.MethodPost, "/api/v1/admin/aliases", `{"source_address":"upstream@required.test","destination_address":"reports@external.example"}`)
	if response.Code != http.StatusCreated {
		t.Fatalf("forwarding target alias: %d %s", response.Code, response.Body)
	}
	response = postmasterRequest(t, aliases, claims, http.MethodPatch, aliasPath, `{"destination_address":"upstream@required.test"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("replacement alias chain: %d %s", response.Code, response.Body)
	}
	accounts := accountRoutes(NewAccountHandler(database, cache, ""))
	refused(accounts, http.MethodPost, "/api/v1/accounts", `{"address":"upstream@required.test","receive":false}`)
}
