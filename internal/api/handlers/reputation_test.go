package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/api/middleware"
	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/grants"
)

// The reputation view's figures come from what the server already records
// (#1590): received DMARC reports, the outbound queue and the DNSBL monitor's
// events. Two domains with seeded data get their own counts and rates, sorted
// worst bounce rate first; a domain with no data gets zeros and nulls; and only
// queue:read at the server's own scope may read it, not a domain's administrator.
func TestReputation_FiguresPerDomain(t *testing.T) {
	db := openMsgDeleteTestDB(t)
	if err := db.AutoMigrate(&models.OutboundQueue{}, &models.DMARCReportReceived{}, &models.DNSBLListingEvent{}); err != nil {
		t.Skipf("reputation DB test skipped: migrate failed (%v)", err)
	}
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })
	// Only this test's rows count.
	for _, table := range []string{"outbound_queue", "dmarc_reports_received", "dnsbl_listing_events"} {
		if err := tx.Exec("DELETE FROM " + table).Error; err != nil {
			t.Fatal(err)
		}
	}

	now := time.Now().UTC()
	clean, noisy, quiet := models.Domain{Name: "clean-rep.test"}, models.Domain{Name: "noisy-rep.test"}, models.Domain{Name: "quiet-rep.test"}
	for _, d := range []*models.Domain{&clean, &noisy, &quiet} {
		if err := tx.Create(d).Error; err != nil {
			t.Fatal(err)
		}
	}
	stoppedAt := now.Add(-time.Hour)
	if err := tx.Model(&noisy).Updates(map[string]any{"outbound_stopped_at": stoppedAt, "outbound_stop_reason": "bounce spike", "outbound_stopped_by": "op@example.test"}).Error; err != nil {
		t.Fatal(err)
	}

	queue := func(sender, status string, category models.FailureCategory, age time.Duration) {
		t.Helper()
		row := models.OutboundQueue{Sender: sender, Recipient: "r@remote.test", Domain: "remote.test", Status: status, FailureCategory: category}
		if err := tx.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
		if err := tx.Model(&row).Update("created_at", now.Add(-age)).Error; err != nil {
			t.Fatal(err)
		}
	}
	day := 24 * time.Hour
	// clean: 3 delivered, 1 bounced (bad_mailbox) this week; nothing the week before.
	for range 3 {
		queue("a@clean-rep.test", "delivered", "", day)
	}
	queue("a@clean-rep.test", "bounced", models.FailureBadMailbox, 2*day)
	// noisy: 1 delivered, 2 policy bounces, 1 expired, 1 deferred this week (sender case
	// differs: the domain is matched case-insensitively); 4 delivered the week before.
	queue("x@Noisy-Rep.test", "delivered", "", day)
	queue("x@noisy-rep.test", "bounced", models.FailurePolicy, day)
	queue("x@noisy-rep.test", "bounced", models.FailurePolicy, 3*day)
	queue("x@noisy-rep.test", "expired", models.FailureConnection, 6*day)
	queue("x@noisy-rep.test", "deferred", models.FailureGreylisted, day)
	for range 4 {
		queue("x@noisy-rep.test", "delivered", "", 10*day)
	}
	// Older than both windows: not counted.
	queue("x@noisy-rep.test", "bounced", models.FailurePolicy, 20*day)

	report := func(domain models.Domain, id string, end time.Time, records ...models.DMARCReceivedRecord) {
		t.Helper()
		r := models.DMARCReportReceived{DomainID: domain.ID, Reporter: "rx.test", ReportingOrg: "rx", ReportID: id,
			StartDate: end.Add(-day), EndDate: end, PolicyPublished: models.DMARCReceivedPolicy{Domain: domain.Name, P: "none"},
			Records: records, ReceivedAt: end}
		if err := tx.Create(&r).Error; err != nil {
			t.Fatal(err)
		}
	}
	record := func(count int64, dkim, spf string) models.DMARCReceivedRecord {
		return models.DMARCReceivedRecord{Row: models.DMARCReceivedRow{SourceIP: "203.0.113.10", Count: count,
			PolicyEvaluated: models.DMARCReceivedEvaluation{Disposition: "none", DKIM: dkim, SPF: spf}}}
	}
	report(clean, "c1", now.Add(-day), record(8, "pass", "fail"), record(2, "fail", "pass"))
	report(noisy, "n1", now.Add(-2*day), record(3, "pass", "pass"), record(9, "fail", "fail"))
	report(noisy, "n0", now.Add(-9*day), record(5, "pass", "pass"))

	dnsbl := func(kind string, id uint64, at time.Time) {
		t.Helper()
		e := models.DNSBLListingEvent{ID: id, IP: "203.0.113.10", Provider: "example", Zone: "bl.example.org", Kind: kind,
			At: at, Since: now.Add(-3 * day), Codes: json.RawMessage(`["127.0.0.2"]`), Reason: "listed for spam", Query: "10.113.0.203.bl.example.org"}
		if err := tx.Create(&e).Error; err != nil {
			t.Fatal(err)
		}
	}
	dnsbl("listed", 9_000_001, now.Add(-3*day))
	// A pair listed and later delisted no longer stands.
	e := models.DNSBLListingEvent{ID: 9_000_002, IP: "203.0.113.11", Provider: "example", Zone: "bl.example.org", Kind: "listed",
		At: now.Add(-5 * day), Since: now.Add(-5 * day), Codes: json.RawMessage(`[]`)}
	if err := tx.Create(&e).Error; err != nil {
		t.Fatal(err)
	}
	e.ID, e.Kind, e.At = 9_000_003, "delisted", now.Add(-4*day)
	if err := tx.Create(&e).Error; err != nil {
		t.Fatal(err)
	}

	const operator, domainAdmin uint = 9101, 9102
	held := func(account uint) (grants.Held, error) {
		if account == operator {
			return grants.HeldOf(grants.Entry{Scope: grants.All, Capability: middleware.CapQueueRead}), nil
		}
		return grants.HeldOf(
			grants.Entry{Scope: grants.Domain(noisy.Name), Capability: middleware.CapQueueRead},
			grants.Entry{Scope: grants.Domain(noisy.Name), Capability: middleware.CapDomainsRead},
		), nil
	}
	routes := chi.NewRouter()
	NewReputationHandler(tx).Register(endpoint.Routes{Router: routes, Registry: endpoint.NewRegistry()}, func(capability string) endpoint.Middleware {
		return endpoint.Middleware{Wrap: middleware.RequireCapability(held, capability, nil)}
	})
	call := func(account uint, query string) (int, Reputation) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/reputation"+query, nil)
		req = req.WithContext(context.WithValue(req.Context(), middleware.ClaimsKey, &auth.Claims{AccountID: account}))
		rr := httptest.NewRecorder()
		routes.ServeHTTP(rr, req)
		var answer struct {
			Data Reputation `json:"data"`
		}
		if rr.Code == http.StatusOK {
			if err := json.Unmarshal(rr.Body.Bytes(), &answer); err != nil {
				t.Fatalf("decode %s: %v", rr.Body, err)
			}
		}
		return rr.Code, answer.Data
	}

	if code, _ := call(domainAdmin, ""); code != http.StatusForbidden {
		t.Fatalf("a domain administrator reading the reputation view: %d; want 403", code)
	}
	code, got := call(operator, "")
	if code != http.StatusOK {
		t.Fatalf("the operator reading the reputation view: %d", code)
	}
	if got.Days != 7 || got.To.Sub(got.From) != 7*day {
		t.Fatalf("default window: %d days, %s to %s; want 7 days", got.Days, got.From, got.To)
	}
	if !got.DNSBL.Listed || len(got.DNSBL.Listings) != 1 || got.DNSBL.Listings[0].IP != "203.0.113.10" || got.DNSBL.Listings[0].Reason != "listed for spam" {
		t.Fatalf("DNSBL state: %+v; want the one listing that stands", got.DNSBL)
	}

	byName := map[string]DomainReputation{}
	var order []string
	for _, d := range got.Domains {
		byName[d.Domain] = d
		order = append(order, d.Domain)
	}
	var mine []string
	for _, name := range order {
		if name == clean.Name || name == noisy.Name || name == quiet.Name {
			mine = append(mine, name)
		}
	}
	if len(mine) != 3 || mine[0] != noisy.Name || mine[1] != clean.Name || mine[2] != quiet.Name {
		t.Fatalf("domain order: %v; want noisy (3/4 bounced), clean (1/4), then quiet (no mail)", mine)
	}

	rate := func(r *float64) float64 {
		if r == nil {
			return -1
		}
		return *r
	}
	n := byName[noisy.Name]
	if o := n.Current.Outbound; o.Delivered != 1 || o.Bounced != 3 || o.Deferred != 1 || rate(o.BounceRate) != 0.75 ||
		o.Bounces != (ReputationBounces{Policy: 2, Expired: 1}) {
		t.Fatalf("noisy outbound this week: %+v rate %v; want 1 delivered, 3 bounced (2 policy, 1 expired), 1 deferred, rate 0.75", o, rate(o.BounceRate))
	}
	if o := n.Previous.Outbound; o.Delivered != 4 || o.Bounced != 0 || rate(o.BounceRate) != 0 {
		t.Fatalf("noisy outbound the week before: %+v; want 4 delivered, rate 0", o)
	}
	if d := n.Current.DMARC; d.Pass != 3 || d.Fail != 9 || rate(d.PassRate) != 0.25 {
		t.Fatalf("noisy DMARC this week: %+v rate %v; want 3 pass, 9 fail, 0.25", d, rate(d.PassRate))
	}
	if d := n.Previous.DMARC; d.Pass != 5 || d.Fail != 0 || rate(d.PassRate) != 1 {
		t.Fatalf("noisy DMARC the week before: %+v; want 5 pass", d)
	}
	if n.OutboundStop == nil || n.OutboundStop.Reason != "bounce spike" || n.Current.Complaints != nil {
		t.Fatalf("noisy stop and complaints: %+v, %v; want its stop and null complaints", n.OutboundStop, n.Current.Complaints)
	}

	c := byName[clean.Name]
	if o := c.Current.Outbound; o.Delivered != 3 || o.Bounced != 1 || rate(o.BounceRate) != 0.25 || o.Bounces != (ReputationBounces{BadMailbox: 1}) {
		t.Fatalf("clean outbound this week: %+v; want 3 delivered, 1 bad_mailbox bounce, rate 0.25", o)
	}
	if d := c.Current.DMARC; d.Pass != 10 || d.Fail != 0 || rate(d.PassRate) != 1 {
		t.Fatalf("clean DMARC this week: %+v; want 10 pass (DKIM or SPF passing), 0 fail", d)
	}
	if c.OutboundStop != nil || c.Previous.Outbound.BounceRate != nil || c.Previous.DMARC.PassRate != nil {
		t.Fatalf("clean stop and previous week: %+v, %+v; want no stop and null rates", c.OutboundStop, c.Previous)
	}

	q := byName[quiet.Name]
	if q.Current != (ReputationFigures{}) || q.Previous != (ReputationFigures{}) || q.OutboundStop != nil {
		t.Fatalf("a domain with no data: %+v; want zeros and nulls", q)
	}

	// A 30-day window takes in the noisy domain's earlier week too.
	if code, got := call(operator, "?days=30"); code != http.StatusOK || got.Days != 30 {
		t.Fatalf("a 30-day window: %d, %d days", code, got.Days)
	} else {
		for _, d := range got.Domains {
			if d.Domain == noisy.Name && (d.Current.Outbound.Delivered != 5 || d.Current.Outbound.Bounced != 4) {
				t.Fatalf("noisy over 30 days: %+v; want 5 delivered, 4 bounced", d.Current.Outbound)
			}
		}
	}
	if code, _ := call(operator, "?days=91"); code != http.StatusUnprocessableEntity {
		t.Fatalf("a 91-day window: %d; want 422", code)
	}
}
