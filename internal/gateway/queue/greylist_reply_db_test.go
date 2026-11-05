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

// inboundRoute is a real queue worker whose only way to inboundRouteDomain is the
// SMTP gateway's real inbound port, in front of the real deliver route and steps as
// the domain's inbound pipeline. It returns the worker, the transaction everything
// runs in, and the recipient's mailbox.
func inboundRoute(t *testing.T, steps ...pipeline.FilterConfig) (*Worker, *gorm.DB, uint) {
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
	registry.Register("dmarc_reports_received", filters.NewDMARCReportsReceived)
	registry.Register("vacation", func([]byte) (pipeline.Filter, error) { return aliasForwardPass{}, nil })
	router := chi.NewRouter()
	routes := endpoint.Routes{Router: router, Registry: endpoint.NewRegistry()}
	handlers.NewMessageHandler(db, nil, pipeline.NewEngine(registry, nil), "", nil).
		RegisterDelivery(routes, endpoint.Middleware{Wrap: func(next http.Handler) http.Handler { return next }})
	handlers.NewMailboxHandler(db, nil, nil).RegisterAddressCheck(routes)
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
	return w, db, mailbox.ID
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
	w, db, mailbox := inboundRoute(t, pipeline.FilterConfig{Name: "greylist", Type: pipeline.FilterTypeAction, Enabled: true,
		Config: json.RawMessage(`{"delay_minutes": 5, "ttl_days": 36}`)})
	item := queueForInboundRoute(t, db)

	now := time.Now()
	err := w.deliver(context.Background(), item)
	reply := refusal(t, err)
	if reply.Code != 450 || reply.Enhanced != "4.7.1" || !strings.Contains(reply.Message, "Greylisted") {
		t.Fatalf("reply %d %s %q, want 450 4.7.1 naming greylisting", reply.Code, reply.Enhanced, reply.Message)
	}
	if got := ClassifyFailure(err); got != models.FailureGreylisted {
		t.Fatalf("reply classified %s, want %s", got, models.FailureGreylisted)
	}
	w.recordFailure(context.Background(), item, err, now)
	row := readRow(t, db, item.ID)
	if row.Status != "deferred" || row.FailureCategory != models.FailureGreylisted || row.LastErrorCode != 450 {
		t.Fatalf("row %s, category %s, code %d: want deferred, greylisted, 450", row.Status, row.FailureCategory, row.LastErrorCode)
	}
	if wait := row.NextAttempt.Sub(now); wait < 5*time.Minute {
		t.Fatalf("first retry after %s: before the five-minute greylist delay ends", wait)
	}

	// A retry before the delay ends is refused the same way.
	if got := ClassifyFailure(w.deliver(context.Background(), row)); got != models.FailureGreylisted {
		t.Fatalf("retry during the delay classified %s, want %s", got, models.FailureGreylisted)
	}
	if err := db.Exec("UPDATE greylist_entries SET retry_after = now() - interval '1 hour'").Error; err != nil {
		t.Fatal(err)
	}
	if err := w.deliver(context.Background(), row); err != nil {
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

// Any other step's deferral says nothing about greylisting and keeps its category.
func TestOtherDeferralThroughInboundPortStaysOtherTransient(t *testing.T) {
	w, db, _ := inboundRoute(t, pipeline.FilterConfig{Name: "rate_limit", Type: pipeline.FilterTypeAction, Enabled: true,
		Config: json.RawMessage(`{"max_per_minute": 1, "max_per_hour": 100}`)})
	if err := w.deliver(context.Background(), queueForInboundRoute(t, db)); err != nil {
		t.Fatalf("first message: %v", err)
	}

	err := w.deliver(context.Background(), queueForInboundRoute(t, db))
	reply := refusal(t, err)
	if reply.Code != 451 || reply.Enhanced != "4.3.0" || reply.Message != "Try again later" {
		t.Fatalf("reply %d %s %q, want 451 4.3.0 Try again later", reply.Code, reply.Enhanced, reply.Message)
	}
	if got := ClassifyFailure(err); got != models.FailureOtherTransient {
		t.Fatalf("reply classified %s, want %s", got, models.FailureOtherTransient)
	}
}
