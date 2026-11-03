package handlers

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/smtp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/gateway/apiclient"
	"github.com/rest-mail/rest-mail-server/internal/gateway/connlimiter"
	smtpgateway "github.com/rest-mail/rest-mail-server/internal/gateway/smtp"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
	pipelinefilters "github.com/rest-mail/rest-mail-server/internal/pipeline/filters"
	"gorm.io/gorm"
)

type aliasDeliveryFixture struct {
	db            *gorm.DB
	domain        models.Domain
	mailbox       models.Mailbox
	alias         models.Alias
	api           *httptest.Server
	smtpAddress   string
	notifications *aliasDeliveryNotifications
	messages      *MessageHandler
	extraDomains  []uint
}

type aliasDeliveryNotifications struct {
	count     atomic.Uint32
	mailboxID atomic.Uint32
}

func (notifier *aliasDeliveryNotifications) NewMail(mailboxID uint) {
	notifier.mailboxID.Store(uint32(mailboxID))
	notifier.count.Add(1)
}

func newAliasDeliveryFixture(t *testing.T) *aliasDeliveryFixture {
	t.Helper()
	database := openRedirectTestDB(t)
	if err := database.AutoMigrate(&models.Attachment{}, &models.SieveScript{}); err != nil {
		t.Fatal(err)
	}
	fixture := &aliasDeliveryFixture{db: database}
	fixture.domain = models.Domain{Name: fmt.Sprintf("alias-%d.test", time.Now().UnixNano()), Active: true}
	if err := database.Create(&fixture.domain).Error; err != nil {
		t.Fatal(err)
	}
	fixture.mailbox = models.Mailbox{DomainID: fixture.domain.ID, LocalPart: "destination", Address: "destination@" + fixture.domain.Name, Active: true, QuotaBytes: 1 << 30}
	if err := database.Create(&fixture.mailbox).Error; err != nil {
		t.Fatal(err)
	}
	seedReceiveFixture(t, database, fixture.mailbox)
	fixture.alias = models.Alias{DomainID: fixture.domain.ID, SourceAddress: "alias@" + fixture.domain.Name, DestinationAddress: fixture.mailbox.Address, Active: true}
	if err := database.Create(&fixture.alias).Error; err != nil {
		t.Fatal(err)
	}
	filters, _ := json.Marshal([]pipeline.FilterConfig{{Name: "recipient_check", Type: pipeline.FilterTypeAction, Enabled: true}, {Name: "sieve", Type: pipeline.FilterTypeTransform, Enabled: true}})
	if err := database.Create(&models.Pipeline{DomainID: fixture.domain.ID, Direction: "inbound", Active: true, Filters: filters}).Error; err != nil {
		t.Fatal(err)
	}
	filterRegistry := withQuietVacation(pipeline.NewRegistry())
	filterRegistry.Register("recipient_check", pipelinefilters.NewRecipientCheck(database))
	filterRegistry.Register("sieve", pipelinefilters.NewSieve)
	engine := pipeline.NewEngine(filterRegistry, nil)
	routes := chi.NewRouter()
	registry := endpoint.NewRegistry()
	mailboxes := NewMailboxHandler(database, nil, nil)
	mailboxes.RegisterAddressCheck(endpoint.Routes{Router: routes, Registry: registry})
	messages := NewMessageHandler(database, nil, engine, "", nil)
	fixture.messages = messages
	fixture.notifications = &aliasDeliveryNotifications{}
	messages.SetNewMailNotifier(fixture.notifications)
	messages.RegisterDelivery(endpoint.Routes{Router: routes, Registry: registry}, endpoint.Middleware{Wrap: func(next http.Handler) http.Handler { return next }})
	rest := NewRestmailHandler(database, engine, nil, RestmailTarpitConfig{}, RestmailDeliverAuthConfig{TrustedCIDRs: []string{"127.0.0.0/8"}})
	rest.SetLocalDelivery(messages)
	rest.Register(endpoint.Routes{Router: routes, Registry: registry}, endpoint.Middleware{Wrap: func(next http.Handler) http.Handler { return next }})
	fixture.api = httptest.NewServer(routes)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	server := smtpgateway.NewServer("mx."+fixture.domain.Name, apiclient.New(fixture.api.URL), nil, smtpgateway.NewStore(database, ""), connlimiter.New(connlimiter.Config{}))
	if err := server.ListenAndServe(smtpgateway.SMTPPorts{Inbound: port}); err != nil {
		t.Fatal(err)
	}
	fixture.smtpAddress = fmt.Sprintf("127.0.0.1:%d", port)
	t.Cleanup(func() {
		server.Shutdown()
		fixture.api.Close()
		domains := append(fixture.extraDomains, fixture.domain.ID)
		database.Where("sending_domain = ?", fixture.domain.Name).Delete(&models.OutboundQueue{})
		database.Where("mailbox_id = ?", fixture.mailbox.ID).Delete(&models.Message{})
		database.Where("mailbox_id = ?", fixture.mailbox.ID).Delete(&models.SieveScript{})
		database.Where("domain_id IN ?", domains).Delete(&models.Alias{})
		database.Where("domain_id IN ?", domains).Delete(&models.Pipeline{})
		database.Where("id = ?", fixture.mailbox.ID).Delete(&models.Mailbox{})
		database.Where("id IN ?", domains).Delete(&models.Domain{})
	})
	return fixture
}

func (fixture *aliasDeliveryFixture) sendSMTP(t *testing.T, recipients ...string) error {
	t.Helper()
	client, err := smtp.Dial(fixture.smtpAddress)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	if err := client.Mail("sender@outside.test"); err != nil {
		t.Fatal(err)
	}
	for _, recipient := range recipients {
		if err := client.Rcpt(recipient); err != nil {
			return fmt.Errorf("RCPT %s: %w", recipient, err)
		}
	}
	writer, err := client.Data()
	if err != nil {
		return err
	}
	fmt.Fprintf(writer, "From: sender@outside.test\r\nTo: %s\r\nSubject: alias delivery\r\nMessage-ID: <alias-%d@outside.test>\r\n\r\nbody\r\n", strings.Join(recipients, ", "), time.Now().UnixNano())
	if err := writer.Close(); err != nil {
		return fmt.Errorf("DATA: %w", err)
	}
	return client.Quit()
}

func TestAliasDeliverySMTPHosted(t *testing.T) {
	fixture := newAliasDeliveryFixture(t)
	if err := fixture.sendSMTP(t, fixture.alias.SourceAddress); err != nil {
		t.Fatal(err)
	}
	var count int64
	fixture.db.Model(&models.Message{}).Where("mailbox_id = ?", fixture.mailbox.ID).Count(&count)
	if count != 1 {
		t.Fatalf("destination messages = %d, want 1", count)
	}
}

func TestAliasDeliverySMTPLoopRefusedBeforeAcceptance(t *testing.T) {
	fixture := newAliasDeliveryFixture(t)
	if err := fixture.db.Model(&fixture.alias).Update("destination_address", fixture.alias.SourceAddress).Error; err != nil {
		t.Fatal(err)
	}
	client, err := smtp.Dial(fixture.smtpAddress)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	if err := client.Mail("sender@outside.test"); err != nil {
		t.Fatal(err)
	}
	if err := client.Rcpt(fixture.alias.SourceAddress); err == nil || !strings.Contains(err.Error(), "Alias delivery is not available for this recipient.") {
		t.Fatalf("alias loop must be refused safely at RCPT, got %v", err)
	}
}

func TestAliasDeliverySMTPChangedLoopRefusesBeforePartialCommit(t *testing.T) {
	fixture := newAliasDeliveryFixture(t)
	client, err := smtp.Dial(fixture.smtpAddress)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	if err := client.Mail("sender@outside.test"); err != nil {
		t.Fatal(err)
	}
	for _, address := range []string{fixture.mailbox.Address, fixture.alias.SourceAddress} {
		if err := client.Rcpt(address); err != nil {
			t.Fatal(err)
		}
	}
	if err := fixture.db.Model(&fixture.alias).Update("destination_address", fixture.alias.SourceAddress).Error; err != nil {
		t.Fatal(err)
	}
	writer, err := client.Data()
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprint(writer, "From: sender@outside.test\r\nSubject: changed alias\r\n\r\nbody\r\n")
	if err := writer.Close(); err == nil || !strings.Contains(err.Error(), "Alias delivery is not available for this recipient.") {
		t.Fatalf("loop accepted or unclear refusal: %v", err)
	}
	var count int64
	if err := fixture.db.Model(&models.Message{}).Where("mailbox_id = ?", fixture.mailbox.ID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("DATA refusal after partial storage: %d messages", count)
	}
}

func (fixture *aliasDeliveryFixture) sendRESTMAIL(t *testing.T, recipients ...string) error {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"from": "sender@outside.test", "to": recipients,
		"raw_message": "From: sender@outside.test\r\nTo: " + strings.Join(recipients, ", ") + "\r\nSubject: alias delivery\r\n\r\nbody\r\n"})
	response, err := http.Post(fixture.api.URL+"/restmail/messages", "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var result json.RawMessage
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusCreated {
		return fmt.Errorf("RESTMAIL %d: %s", response.StatusCode, result)
	}
	var delivered struct {
		Data RestmailDelivery `json:"data"`
	}
	if err := json.Unmarshal(result, &delivered); err != nil {
		t.Fatal(err)
	}
	if len(delivered.Data.Failed) != 0 || len(delivered.Data.Delivered) != len(recipients) {
		return fmt.Errorf("RESTMAIL recipients: %s", result)
	}
	return nil
}

func TestAliasDeliveryRESTMAILStructured(t *testing.T) {
	for _, destination := range []string{"hosted", "external"} {
		t.Run(destination, func(t *testing.T) {
			fixture := newAliasDeliveryFixture(t)
			if destination == "external" {
				if err := fixture.db.Model(&fixture.alias).Update("destination_address", "destination@elsewhere.test").Error; err != nil {
					t.Fatal(err)
				}
			}
			body, err := json.Marshal(map[string]any{"from": "sender@outside.test", "to": []string{fixture.alias.SourceAddress}, "subject": "structured alias", "body_text": "structured body"})
			if err != nil {
				t.Fatal(err)
			}
			response, err := http.Post(fixture.api.URL+"/restmail/messages", "application/json", strings.NewReader(string(body)))
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if destination == "external" {
				if response.StatusCode != http.StatusUnprocessableEntity {
					t.Fatalf("external structured delivery = %d, want 422", response.StatusCode)
				}
				var queued int64
				if err := fixture.db.Model(&models.OutboundQueue{}).Where("sending_domain = ?", fixture.domain.Name).Count(&queued).Error; err != nil || queued != 0 {
					t.Fatalf("structured external queue = %d, %v", queued, err)
				}
				return
			}
			if response.StatusCode != http.StatusCreated {
				t.Fatalf("hosted structured delivery = %d, want 201", response.StatusCode)
			}
			var message models.Message
			if err := fixture.db.Where("mailbox_id = ?", fixture.mailbox.ID).First(&message).Error; err != nil {
				t.Fatal(err)
			}
			if message.Subject != "structured alias" || message.BodyText != "structured body" || message.RawMessage != "" {
				t.Fatalf("structured content changed: subject=%q body=%q raw=%q", message.Subject, message.BodyText, message.RawMessage)
			}
		})
	}
}

func TestAliasDeliveryRoutes(t *testing.T) {
	for _, route := range []string{"SMTP", "RESTMAIL"} {
		for _, scenario := range []string{"hosted", "external", "inactive", "chain", "loop", "too long", "plain mailbox", "mailbox wins", "alias wins over plus", "inactive reserves plus", "missing hosted destination", "ambiguous alias", "destination quota", "destination pipeline", "external spelling", "quoted external", "ambiguous legacy alias", "cross domain hosted", "inactive mailbox wins"} {
			t.Run(route+"/"+scenario, func(t *testing.T) {
				fixture := newAliasDeliveryFixture(t)
				address := fixture.alias.SourceAddress
				wantError, wantLocal, wantQueued := false, int64(1), int64(0)
				forwardTarget := "destination@elsewhere.test"
				switch scenario {
				case "external":
					fixture.db.Model(&fixture.alias).Update("destination_address", "destination@elsewhere.test")
					wantLocal, wantQueued = 0, 1
				case "inactive", "inactive reserves plus":
					fixture.db.Model(&fixture.alias).Update("active", false)
					if scenario == "inactive reserves plus" {
						address = "destination+tag@" + fixture.domain.Name
						fixture.db.Model(&fixture.alias).Update("source_address", address)
					}
					wantError, wantLocal = true, 0
				case "chain", "loop", "too long":
					length := 8
					if scenario == "too long" {
						length = 9
					}
					for index := 1; index < length; index++ {
						source := fmt.Sprintf("chain%d@%s", index, fixture.domain.Name)
						destination := fixture.mailbox.Address
						if index < length-1 {
							destination = fmt.Sprintf("chain%d@%s", index+1, fixture.domain.Name)
						}
						if scenario == "loop" && index == length-1 {
							destination = address
						}
						if err := fixture.db.Create(&models.Alias{DomainID: fixture.domain.ID, SourceAddress: source, DestinationAddress: destination, Active: true}).Error; err != nil {
							t.Fatal(err)
						}
					}
					fixture.db.Model(&fixture.alias).Update("destination_address", "chain1@"+fixture.domain.Name)
					if scenario != "chain" {
						wantError, wantLocal = true, 0
					}
				case "plain mailbox", "mailbox wins":
					address = fixture.mailbox.Address
					if scenario == "mailbox wins" {
						fixture.db.Model(&fixture.alias).Updates(map[string]any{"source_address": address, "destination_address": "destination@elsewhere.test"})
					}
				case "alias wins over plus":
					address = "destination+tag@" + fixture.domain.Name
					fixture.db.Model(&fixture.alias).Updates(map[string]any{"source_address": address, "destination_address": "destination@elsewhere.test"})
					wantLocal, wantQueued = 0, 1
				case "missing hosted destination":
					fixture.db.Model(&fixture.alias).Update("destination_address", "missing@"+fixture.domain.Name)
					wantError, wantLocal = true, 0
				case "ambiguous alias":
					if err := fixture.db.Create(&models.Alias{DomainID: fixture.domain.ID, SourceAddress: address, DestinationAddress: "other@elsewhere.test", Active: true}).Error; err != nil {
						t.Fatal(err)
					}
					wantError, wantLocal = true, 0
				case "destination quota":
					if err := fixture.db.Model(&fixture.mailbox).Updates(map[string]any{"quota_bytes": 1, "quota_used_bytes": 1}).Error; err != nil {
						t.Fatal(err)
					}
					wantError, wantLocal = true, 0
				case "destination pipeline":
					script := `require ["fileinto"]; fileinto "Alias destination";`
					if err := fixture.db.Create(&models.SieveScript{MailboxID: fixture.mailbox.ID, Script: script, ScriptHash: pipelinefilters.HashSieveScript(script), Active: true}).Error; err != nil {
						t.Fatal(err)
					}
				case "external spelling", "quoted external":
					destination := `Team <MiXeD@Elsewhere.test>`
					forwardTarget = "MiXeD@Elsewhere.test"
					if scenario == "quoted external" {
						destination = `"admin team"@elsewhere.test`
						forwardTarget = destination
					}
					if err := fixture.db.Model(&fixture.alias).Update("destination_address", destination).Error; err != nil {
						t.Fatal(err)
					}
					wantLocal, wantQueued = 0, 1
				case "ambiguous legacy alias":
					if err := fixture.db.Create(&models.Alias{DomainID: fixture.domain.ID, SourceAddress: strings.ToUpper(address), DestinationAddress: "other@elsewhere.test", Active: true}).Error; err != nil {
						t.Fatal(err)
					}
					wantError, wantLocal = true, 0
				case "cross domain hosted":
					other := models.Domain{Name: "destination." + fixture.domain.Name, Active: true}
					if err := fixture.db.Create(&other).Error; err != nil {
						t.Fatal(err)
					}
					fixture.extraDomains = append(fixture.extraDomains, other.ID)
					fixture.mailbox.DomainID, fixture.mailbox.Address = other.ID, "destination@"+other.Name
					if err := fixture.db.Save(&fixture.mailbox).Error; err != nil {
						t.Fatal(err)
					}
					seedReceiveFixture(t, fixture.db, fixture.mailbox)
					if err := fixture.db.Model(&fixture.alias).Update("destination_address", fixture.mailbox.Address).Error; err != nil {
						t.Fatal(err)
					}
					policy, _ := json.Marshal([]pipeline.FilterConfig{{Name: "recipient_check", Type: pipeline.FilterTypeAction, Enabled: true}, {Name: "sieve", Type: pipeline.FilterTypeTransform, Enabled: true, Config: json.RawMessage(`{"script":"require [\"fileinto\"]; fileinto \"Other domain\";"}`)}})
					if err := fixture.db.Create(&models.Pipeline{DomainID: other.ID, Direction: "inbound", Active: true, Filters: policy}).Error; err != nil {
						t.Fatal(err)
					}
				case "inactive mailbox wins":
					address = fixture.mailbox.Address
					if err := fixture.db.Model(&fixture.mailbox).Update("active", false).Error; err != nil {
						t.Fatal(err)
					}
					if err := fixture.db.Model(&fixture.alias).Updates(map[string]any{"source_address": address, "destination_address": "destination@elsewhere.test"}).Error; err != nil {
						t.Fatal(err)
					}
					wantError, wantLocal = true, 0
				}
				var err error
				if route == "SMTP" {
					err = fixture.sendSMTP(t, address)
				} else {
					err = fixture.sendRESTMAIL(t, address)
				}
				if (err != nil) != wantError {
					t.Fatalf("delivery = %v, want error %v", err, wantError)
				}
				if (scenario == "loop" || scenario == "too long") && !strings.Contains(err.Error(), "Alias delivery is not available for this recipient.") {
					t.Fatalf("unsafe or unclear alias refusal: %v", err)
				}
				var local, queued int64
				fixture.db.Model(&models.Message{}).Where("mailbox_id = ?", fixture.mailbox.ID).Count(&local)
				fixture.db.Model(&models.OutboundQueue{}).Where("sending_domain = ?", fixture.domain.Name).Count(&queued)
				if local != wantLocal || queued != wantQueued {
					t.Fatalf("local=%d queued=%d, want %d/%d", local, queued, wantLocal, wantQueued)
				}
				if scenario == "destination pipeline" {
					var message models.Message
					if err := fixture.db.Where("mailbox_id = ?", fixture.mailbox.ID).First(&message).Error; err != nil {
						t.Fatal(err)
					}
					if message.Folder != "Alias destination" {
						t.Fatalf("destination script not run: folder %s", message.Folder)
					}
				}
				if scenario == "cross domain hosted" {
					var message models.Message
					if err := fixture.db.Where("mailbox_id = ?", fixture.mailbox.ID).First(&message).Error; err != nil {
						t.Fatal(err)
					}
					if message.Folder != "Other domain" {
						t.Fatalf("destination domain policy not run: %s", message.Folder)
					}
				}
				if scenario == "hosted" || (route == "SMTP" && wantLocal == 1 && scenario != "destination pipeline" && scenario != "cross domain hosted") {
					if fixture.notifications.count.Load() != 1 || fixture.notifications.mailboxID.Load() != uint32(fixture.mailbox.ID) {
						t.Fatal("destination inbox notification missing")
					}
				}
				if wantQueued == 1 {
					var row models.OutboundQueue
					if err := fixture.db.Where("sending_domain = ?", fixture.domain.Name).First(&row).Error; err != nil {
						t.Fatal(err)
					}
					if row.ARCResults == "" || row.Sender != "sender@outside.test" || row.Recipient != forwardTarget {
						t.Fatalf("not a Sieve-compatible forward: %+v", row)
					}
				}
			})
		}
	}
}
