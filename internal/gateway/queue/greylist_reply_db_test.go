package queue

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rest-mail/go-mtasts/pkg/mtasts"
	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/api/handlers"
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

const inboundRouteDomain = "greylist-reply.test"

// inboundReceiver is a receiving server for inboundRouteDomain: its real delivery
// routes, with steps as the domain's inbound pipeline. It returns the router, the
// transaction everything runs in, and the recipient's mailbox.
func inboundReceiver(t *testing.T, steps ...pipeline.FilterConfig) (*chi.Mux, *gorm.DB, uint) {
	t.Helper()
	base := restmailTestDB(t, inboundRouteDomain)
	if err := bootstrap.EnsureRBAC(base); err != nil {
		t.Fatal(err)
	}
	db := base.Begin()
	t.Cleanup(func() { db.Rollback() })

	domain := models.Domain{Name: inboundRouteDomain, Active: true}
	if err := db.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	mailbox := models.Mailbox{DomainID: domain.ID, LocalPart: "bob", Address: "bob@" + inboundRouteDomain, Active: true}
	if err := db.Create(&mailbox).Error; err != nil {
		t.Fatal(err)
	}
	if err := grants.EnsureReceive(db, &mailbox); err != nil {
		t.Fatal(err)
	}
	stored, err := json.Marshal(steps)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.Pipeline{DomainID: domain.ID, Direction: "inbound", Active: true, Filters: stored}).Error; err != nil {
		t.Fatal(err)
	}

	registry := pipeline.NewRegistry()
	registry.Register("greylist", filters.NewGreylist(db))
	registry.Register("rate_limit", filters.NewRateLimit())
	registry.Register("rspamd", filters.NewRspamd)
	registry.Register("dmarc_reports_received", filters.NewDMARCReportsReceived)
	registry.Register("vacation", func([]byte) (pipeline.Filter, error) { return aliasForwardPass{}, nil })
	router := chi.NewRouter()
	routes := endpoint.Routes{Router: router, Registry: endpoint.NewRegistry()}
	middleware := endpoint.Middleware{Wrap: func(next http.Handler) http.Handler { return next }}
	engine := pipeline.NewEngine(registry, nil)
	messages := handlers.NewMessageHandler(db, nil, engine, "", nil)
	messages.RegisterDelivery(routes, middleware)
	handlers.NewMailboxHandler(db, nil, nil).RegisterAddressCheck(routes)
	rest := handlers.NewRestmailHandler(db, engine, nil, handlers.RestmailTarpitConfig{},
		handlers.RestmailDeliverAuthConfig{TrustedCIDRs: []string{"127.0.0.0/8"}})
	rest.SetLocalDelivery(messages)
	rest.Register(routes, middleware)
	return router, db, mailbox.ID
}

// inboundRoute is a real queue worker whose only way to inboundRouteDomain is the
// SMTP gateway's real inbound port, in front of inboundReceiver. It returns the
// worker, the transaction everything runs in, and the recipient's mailbox.
func inboundRoute(t *testing.T, steps ...pipeline.FilterConfig) (*Worker, *gorm.DB, uint) {
	t.Helper()
	router, db, mailbox := inboundReceiver(t, steps...)
	apiServer := httptest.NewServer(router)
	t.Cleanup(apiServer.Close)

	free, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := free.Addr().(*net.TCPAddr).Port
	_ = free.Close()
	gateway := smtpgateway.NewServer("mx."+inboundRouteDomain, apiclient.New(apiServer.URL), nil,
		smtpgateway.NewStore(db, ""), connlimiter.New(connlimiter.Config{}))
	if err := gateway.ListenAndServe(smtpgateway.SMTPPorts{Inbound: port}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(gateway.Shutdown)

	// The inbound port advertises RESTMAIL: this route is the SMTP hop.
	cacheRESTMAIL(t, db, inboundRouteDomain, false)
	w := NewWorker(db, "sender.test", 1, time.Second)
	w.allowPrivateDest = true
	w.resolver = fixedMXResolver{host: "mx." + inboundRouteDomain}
	w.smtpAddr = func(string) string { return net.JoinHostPort("127.0.0.1", strconv.Itoa(port)) }
	w.sts.LookupTXT = func(context.Context, string) ([]string, error) { return nil, mtasts.ErrNoPolicy }
	return w, db, mailbox
}

// restmailInboundRoute is a real queue worker that reaches inboundReceiver's real
// /restmail/messages over HTTPS, with the domain cached as a REST Mail peer. The
// returned MX counts a message sent by SMTP instead.
func restmailInboundRoute(t *testing.T, steps ...pipeline.FilterConfig) (*Worker, *gorm.DB, uint, *noFallbackMX) {
	t.Helper()
	router, db, mailbox := inboundReceiver(t, steps...)
	var server *httptest.Server
	router.Get("/.well-known/restmail", func(rw http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(rw).Encode(map[string]string{"endpoint": server.URL + "/restmail"})
	})
	server = httptest.NewTLSServer(router)
	t.Cleanup(server.Close)

	mx := newNoFallbackMX(t, true)
	cacheRESTMAIL(t, db, inboundRouteDomain, true)
	w := NewWorker(db, "sender.test", 1, time.Second)
	w.allowPrivateDest = true
	w.resolver = fixedMXResolver{host: "mx." + inboundRouteDomain}
	w.smtpAddr = func(string) string { return mx.addr }
	w.restmailRoots = server.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
	w.restmailDiscoveryURL = func(_, domain string) string { return server.URL + "/.well-known/restmail?domain=" + domain }
	w.sts.LookupTXT = func(context.Context, string) ([]string, error) { return nil, mtasts.ErrNoPolicy }
	return w, db, mailbox, mx
}

// rateLimitStep lets one message through each minute. rspamdDownStep defers
// everything: no scanner answers, and its fallback is to defer. Neither its fault
// nor its verdict says greylisting or a rate limit.
var (
	greylistStep   = pipeline.FilterConfig{Name: "greylist", Type: pipeline.FilterTypeAction, Enabled: true, Config: json.RawMessage(`{"delay_minutes": 5, "ttl_days": 36}`)}
	rateLimitStep  = pipeline.FilterConfig{Name: "rate_limit", Type: pipeline.FilterTypeAction, Enabled: true, Config: json.RawMessage(`{"max_per_minute": 1, "max_per_hour": 100}`)}
	rspamdDownStep = pipeline.FilterConfig{Name: "rspamd", Type: pipeline.FilterTypeAction, Enabled: true,
		Config: json.RawMessage(`{"url": "http://127.0.0.1:1", "timeout_ms": 1000, "fallback_action": "defer"}`)}
)

// checkDeferredRow records the failure as the worker does and checks the row it
// leaves: still queued, in category, retried no sooner than earliest.
func checkDeferredRow(t *testing.T, w *Worker, db *gorm.DB, item models.OutboundQueue, err error, category models.FailureCategory, code int, earliest time.Duration) models.OutboundQueue {
	t.Helper()
	now := time.Now()
	w.recordFailure(item, err, now)
	row := readRow(t, db, item.ID)
	if row.Status != "deferred" || row.FailureCategory != category || row.LastErrorCode != code {
		t.Fatalf("row %s, category %s, code %d: want deferred, %s, %d", row.Status, row.FailureCategory, row.LastErrorCode, category, code)
	}
	if wait := row.NextAttempt.Sub(now); wait < earliest {
		t.Fatalf("first retry after %s, want at least %s", wait, earliest)
	}
	return row
}

// queueForInboundRoute is the row of a message to the route's mailbox, from outside.
func queueForInboundRoute(t *testing.T, db *gorm.DB) models.OutboundQueue {
	t.Helper()
	now := time.Now()
	item := models.OutboundQueue{Sender: "alice@elsewhere.test", Recipient: "bob@" + inboundRouteDomain, Domain: inboundRouteDomain,
		RawMessage: "From: alice@elsewhere.test\r\nTo: bob@" + inboundRouteDomain + "\r\nSubject: first\r\n" +
			"Date: Thu, 05 Nov 2026 05:00:00 +0000\r\nMessage-ID: <" + strconv.FormatInt(now.UnixNano(), 36) + "@elsewhere.test>\r\n\r\nhello\r\n",
		Status: "delivering", Attempts: 1, NextAttempt: now, ExpiresAt: now.Add(models.QueueLifetime), MaxRetries: 50, CreatedAt: now}
	if err := db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	return item
}

// refusal is the inbound port's reply as the worker read it.
func refusal(t *testing.T, err error) *SMTPError {
	t.Helper()
	var reply *SMTPError
	if !errors.As(err, &reply) {
		t.Fatalf("delivery: %v, want the inbound port's refusal", err)
	}
	return reply
}

// A first message through the greylist step (#1794): the inbound port's reply says
// greylisting, so the queue row waits on the greylisting schedule, and a retry after
// the delay is delivered.
func TestGreylistRefusalThroughInboundPortIsGreylisting(t *testing.T) {
	w, db, mailbox := inboundRoute(t, greylistStep)
	item := queueForInboundRoute(t, db)

	now := time.Now()
	err := w.deliver(item)
	reply := refusal(t, err)
	if reply.Code != 450 || reply.Enhanced != "4.7.1" || !strings.Contains(reply.Message, "Greylisted") {
		t.Fatalf("reply %d %s %q, want 450 4.7.1 naming greylisting", reply.Code, reply.Enhanced, reply.Message)
	}
	if got := ClassifyFailure(err); got != models.FailureGreylisted {
		t.Fatalf("reply classified %s, want %s", got, models.FailureGreylisted)
	}
	w.recordFailure(item, err, now)
	row := readRow(t, db, item.ID)
	if row.Status != "deferred" || row.FailureCategory != models.FailureGreylisted || row.LastErrorCode != 450 {
		t.Fatalf("row %s, category %s, code %d: want deferred, greylisted, 450", row.Status, row.FailureCategory, row.LastErrorCode)
	}
	if wait := row.NextAttempt.Sub(now); wait < 5*time.Minute {
		t.Fatalf("first retry after %s: before the five-minute greylist delay ends", wait)
	}

	// A retry before the delay ends is refused the same way.
	if got := ClassifyFailure(w.deliver(row)); got != models.FailureGreylisted {
		t.Fatalf("retry during the delay classified %s, want %s", got, models.FailureGreylisted)
	}
	if err := db.Exec("UPDATE greylist_entries SET retry_after = now() - interval '1 hour'").Error; err != nil {
		t.Fatal(err)
	}
	if err := w.deliver(row); err != nil {
		t.Fatalf("retry after the delay: %v", err)
	}
	var copies int64
	if err := db.Model(&models.Message{}).Where("mailbox_id = ?", mailbox).Count(&copies).Error; err != nil {
		t.Fatal(err)
	}
	if copies != 1 {
		t.Fatalf("%d messages in the mailbox after the retry, want 1", copies)
	}
}

// A rate_limit step's deferral (#1794): the inbound port's reply says so, and the row
// waits on the rate limit schedule.
func TestRateLimitRefusalThroughInboundPortIsRateLimited(t *testing.T) {
	w, db, _ := inboundRoute(t, rateLimitStep)
	if err := w.deliver(queueForInboundRoute(t, db)); err != nil {
		t.Fatalf("first message: %v", err)
	}

	item := queueForInboundRoute(t, db)
	err := w.deliver(item)
	reply := refusal(t, err)
	if reply.Code != 450 || reply.Enhanced != "4.7.1" || reply.Message != "Too many messages, try again later" {
		t.Fatalf("reply %d %s %q, want 450 4.7.1 Too many messages, try again later", reply.Code, reply.Enhanced, reply.Message)
	}
	checkDeferredRow(t, w, db, item, err, models.FailureRateLimited, 450, 12*time.Minute)
}

// Any other step's deferral says nothing about greylisting or a rate limit, and keeps
// its category.
func TestOtherDeferralThroughInboundPortStaysOtherTransient(t *testing.T) {
	w, db, _ := inboundRoute(t, rspamdDownStep)

	item := queueForInboundRoute(t, db)
	err := w.deliver(item)
	reply := refusal(t, err)
	if reply.Code != 451 || reply.Enhanced != "4.3.0" || reply.Message != "Try again later" {
		t.Fatalf("reply %d %s %q, want 451 4.3.0 Try again later", reply.Code, reply.Enhanced, reply.Message)
	}
	checkDeferredRow(t, w, db, item, err, models.FailureOtherTransient, 451, 4*time.Minute)
}

// A first message through the greylist step over RESTMAIL (#1794): the route's answer
// names greylisting, the row waits on the greylisting schedule, and a retry after the
// delay is delivered, by RESTMAIL.
func TestGreylistDeferralOverRESTMAILIsGreylisting(t *testing.T) {
	w, db, mailbox, mx := restmailInboundRoute(t, greylistStep)
	item := queueForInboundRoute(t, db)

	err := w.deliver(item)
	noSMTP(t, db, item, mx, err, false)
	if reply := refusal(t, err); reply.Code != 450 || reply.Enhanced != "4.7.1" || !strings.Contains(reply.Message, `"code":"greylisted"`) {
		t.Fatalf("reply %d %s %q, want 450 4.7.1 with the route's code greylisted", reply.Code, reply.Enhanced, reply.Message)
	}
	row := checkDeferredRow(t, w, db, item, err, models.FailureGreylisted, 450, 5*time.Minute)

	if err := db.Exec("UPDATE greylist_entries SET retry_after = now() - interval '1 hour'").Error; err != nil {
		t.Fatal(err)
	}
	if err := w.deliver(row); err != nil {
		t.Fatalf("retry after the delay: %v", err)
	}
	var copies int64
	if err := db.Model(&models.Message{}).Where("mailbox_id = ?", mailbox).Count(&copies).Error; err != nil {
		t.Fatal(err)
	}
	if copies != 1 || mx.mails.Load() != 0 {
		t.Fatalf("%d messages in the mailbox, %d by SMTP: want 1, by RESTMAIL", copies, mx.mails.Load())
	}
}

// A rate_limit step's deferral over RESTMAIL has its own category and schedule.
func TestRateLimitDeferralOverRESTMAILIsRateLimited(t *testing.T) {
	w, db, _, mx := restmailInboundRoute(t, rateLimitStep)
	if err := w.deliver(queueForInboundRoute(t, db)); err != nil {
		t.Fatalf("first message: %v", err)
	}

	item := queueForInboundRoute(t, db)
	err := w.deliver(item)
	noSMTP(t, db, item, mx, err, false)
	if reply := refusal(t, err); reply.Code != 450 || reply.Enhanced != "4.7.1" || !strings.Contains(reply.Message, `"code":"rate_limited"`) {
		t.Fatalf("reply %d %s %q, want 450 4.7.1 with the route's code rate_limited", reply.Code, reply.Enhanced, reply.Message)
	}
	checkDeferredRow(t, w, db, item, err, models.FailureRateLimited, 450, 12*time.Minute)
}

// Any other deferral over RESTMAIL is an ordinary transient failure, not greylisting.
func TestOtherDeferralOverRESTMAILIsOtherTransient(t *testing.T) {
	w, db, _, mx := restmailInboundRoute(t, rspamdDownStep)

	item := queueForInboundRoute(t, db)
	err := w.deliver(item)
	noSMTP(t, db, item, mx, err, false)
	if reply := refusal(t, err); reply.Code != 451 || reply.Enhanced != "4.3.0" || !strings.Contains(reply.Message, `"code":"deferred"`) {
		t.Fatalf("reply %d %s %q, want 451 4.3.0 with the route's code deferred", reply.Code, reply.Enhanced, reply.Message)
	}
	checkDeferredRow(t, w, db, item, err, models.FailureOtherTransient, 451, 4*time.Minute)
}

// A delivery to an alias runs its own path through /restmail/messages, which carries
// the reason the same way.
func TestAliasDeferralOverRESTMAILKeepsItsReason(t *testing.T) {
	for _, test := range []struct {
		name     string
		step     pipeline.FilterConfig
		category models.FailureCategory
		code     int
	}{
		{"greylist", greylistStep, models.FailureGreylisted, 450},
		{"other", rspamdDownStep, models.FailureOtherTransient, 451},
	} {
		t.Run(test.name, func(t *testing.T) {
			w, db, _, mx := restmailInboundRoute(t, test.step)
			var domain models.Domain
			if err := db.Where("name = ?", inboundRouteDomain).First(&domain).Error; err != nil {
				t.Fatal(err)
			}
			alias := "list@" + inboundRouteDomain
			if err := db.Create(&models.Alias{DomainID: domain.ID, SourceAddress: alias, DestinationAddress: "elsewhere@target.test", Active: true}).Error; err != nil {
				t.Fatal(err)
			}
			item := queueForInboundRoute(t, db)
			item.Recipient = alias
			if err := db.Save(&item).Error; err != nil {
				t.Fatal(err)
			}

			err := w.deliver(item)
			noSMTP(t, db, item, mx, err, false)
			checkDeferredRow(t, w, db, item, err, test.category, test.code, 4*time.Minute)
		})
	}
}
