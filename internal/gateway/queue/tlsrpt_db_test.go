package queue

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	mtasts "github.com/rest-mail/go-mtasts/pkg/mtasts"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

// openTLSReportDB opens a transaction on the unit-test Postgres with the
// tables TLS-RPT reporting uses, skipping when there is none.
func openTLSReportDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := openQueueTestDB(t)
	if err := db.AutoMigrate(&models.TLSRPTSession{}, &models.OutboundQueue{}, &models.DomainRelay{}); err != nil {
		t.Skipf("TLS-RPT DB test skipped: migrate failed (%v)", err)
	}
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })
	return tx
}

// A day of deliveries to a domain, one of them failing its MTA-STS policy,
// makes one report for the domain the next day, sent to its https: rua with
// the failure in failure-details; a domain that asks by mail gets a report
// queued, signed as REST Mail's domain and sent despite TLS failures; a
// policy whose fetch failed is reported as that failure; a report mail's own
// session is not counted; and once reported a day's
// sessions are gone, while today's stay (RFC 8460 §3 to §5, #726).
func TestTLSReports_ADayOfDeliveries(t *testing.T) {
	tx := openTLSReportDB(t)
	sender := seedSignedDomain(t, tx, relayRouteMasterKey)
	if err := tx.Model(&models.Domain{}).Where("name = ?", sender).Update("active", true).Error; err != nil {
		t.Fatal(err)
	}
	run := time.Now().UnixNano()
	byHTTPS := fmt.Sprintf("https-%d.test", run)
	byMail := fmt.Sprintf("mail-%d.test", run)
	quiet := fmt.Sprintf("quiet-%d.test", run)

	var posted []byte
	var contentType string
	collector := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contentType = r.Header.Get("Content-Type")
		posted, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusCreated)
	}))
	defer collector.Close()

	w := &Worker{db: tx, hostname: "mx." + sender, allowPrivateDest: true, sendDeadline: defaultSendDeadline, masterKey: relayRouteMasterKey,
		stsEnforce: true, tlsReportClient: collector.Client(),
		txtResolver: func(_ context.Context, name string) ([]string, error) {
			switch name {
			case "_smtp._tls." + byHTTPS:
				return []string{"v=TLSRPTv1; rua=" + collector.URL + "/tlsrpt"}, nil
			case "_smtp._tls." + byMail:
				return []string{"v=TLSRPTv1; rua=mailto:tls-reports@" + byMail}, nil
			}
			return nil, &net.DNSError{Err: "no such host", IsNotFound: true}
		}}

	cert, _ := relayTestCertificate(t, false) // nothing vouches for it
	deliver := func(item models.OutboundQueue, policy *mtasts.Policy, stsErr error) error {
		settings, finish := relayTestPeer(t, cert, relayPeerOptions{noAUTH: true})
		err := w.deliverSMTP(net.JoinHostPort(settings.host, strconv.Itoa(settings.port)), settings.host, item, policy, stsErr)
		finish()
		return err
	}
	item := tlsTestItem()
	item.Domain, item.Recipient = byHTTPS, "user@"+byHTTPS
	for i := 0; i < 2; i++ {
		if err := deliver(item, nil, nil); err != nil {
			t.Fatalf("delivery without a policy: %v", err)
		}
	}
	// Under an enforce policy the untrusted certificate is refused.
	enforce := &mtasts.Policy{Version: "STSv1", Mode: mtasts.ModeEnforce, MX: []string{"127.0.0.1"}, MaxAge: 86400}
	if err := deliver(item, enforce, nil); err == nil {
		t.Fatal("an untrusted certificate passed an enforce policy")
	}
	// A policy whose host's certificate fails is not applied, and the
	// session is reported as that failure.
	unverified := fmt.Errorf("%w: %w", mtasts.ErrNoPolicy, fmt.Errorf("%w: %w", mtasts.ErrPolicyFetch, x509.UnknownAuthorityError{}))
	if err := deliver(item, nil, unverified); err != nil {
		t.Fatalf("delivery after a failed policy fetch: %v", err)
	}
	// A report mail's own session is not counted.
	report := item
	report.TLSReport = true
	if err := deliver(report, nil, unverified); err != nil {
		t.Fatalf("report delivery: %v", err)
	}

	today := time.Now().UTC().Format(tlsrptDay)
	for _, s := range []models.TLSRPTSession{
		{Day: today, PolicyDomain: byMail, PolicyType: tlsrptNoPolicyFound, ReceivingMX: "mx." + byMail},
		{Day: today, PolicyDomain: quiet, PolicyType: tlsrptNoPolicyFound, ReceivingMX: "mx." + quiet},
	} {
		if err := tx.Create(&s).Error; err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().Add(26 * time.Hour)
	if err := tx.Create(&models.TLSRPTSession{Day: now.UTC().Format(tlsrptDay), PolicyDomain: byHTTPS, PolicyType: tlsrptNoPolicyFound}).Error; err != nil {
		t.Fatal(err)
	}

	w.sendTLSReports(now)

	if contentType != "application/tlsrpt+gzip" {
		t.Fatalf("POSTed %q, want application/tlsrpt+gzip", contentType)
	}
	zr, err := gzip.NewReader(bytes.NewReader(posted))
	if err != nil {
		t.Fatal(err)
	}
	var got tlsReport
	if err := json.NewDecoder(zr).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got.Policies) != 3 || got.ContactInfo != "tlsrpt@"+sender || got.DateRange.Start != today+"T00:00:00Z" {
		t.Fatalf("report %+v", got)
	}
	none, sts, unfetched := got.Policies[0], got.Policies[1], got.Policies[2]
	if none.Policy.PolicyType != tlsrptNoPolicyFound || none.Summary != (tlsReportSummary{Successful: 2}) {
		t.Errorf("no-policy entry %+v", none)
	}
	if sts.Policy.PolicyType != tlsrptPolicySTS || sts.Summary != (tlsReportSummary{Failed: 1}) || len(sts.FailureDetails) != 1 ||
		sts.FailureDetails[0].ResultType != tlsrptCertNotTrusted || sts.FailureDetails[0].ReceivingMXHostname != "127.0.0.1" ||
		sts.FailureDetails[0].ReceivingIP != "127.0.0.1" || sts.FailureDetails[0].FailedSessionCount != 1 {
		t.Errorf("sts entry %+v", sts)
	}
	if unfetched.Policy.PolicyType != tlsrptPolicySTS || unfetched.Policy.PolicyString != nil || unfetched.Summary != (tlsReportSummary{Failed: 1}) ||
		len(unfetched.FailureDetails) != 1 || unfetched.FailureDetails[0].ResultType != tlsrptSTSWebPKIInvalid {
		t.Errorf("unfetched-policy entry %+v", unfetched)
	}

	var queued []models.OutboundQueue
	tx.Where("recipient = ?", "tls-reports@"+byMail).Find(&queued)
	if len(queued) != 1 || !queued[0].TLSReport || queued[0].SendingDomain != sender || queued[0].Sender != "tlsrpt@"+sender ||
		!strings.Contains(queued[0].RawMessage, "TLS-Report-Domain: "+byMail+"\r\n") {
		t.Errorf("queued report %+v", queued)
	}

	var left []models.TLSRPTSession
	tx.Where("policy_domain IN ?", []string{byHTTPS, byMail, quiet}).Find(&left)
	if len(left) != 1 || left[0].Day != now.UTC().Format(tlsrptDay) {
		t.Errorf("sessions left %+v, want only the day not yet over", left)
	}
}

// A report the domain's only address refuses is kept and tried again in the
// next pass, until a day after its day ends (RFC 8460 §5.5).
func TestTLSReports_RefusedReportIsTriedAgain(t *testing.T) {
	tx := openTLSReportDB(t)
	seedSignedDomain(t, tx, relayRouteMasterKey)
	domain := fmt.Sprintf("refuses-%d.test", time.Now().UnixNano())
	collector := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer collector.Close()
	w := &Worker{db: tx, hostname: "mx.sender.test", tlsReportClient: collector.Client(),
		txtResolver: func(context.Context, string) ([]string, error) {
			return []string{"v=TLSRPTv1; rua=" + collector.URL}, nil
		}}
	day := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	if err := tx.Create(&models.TLSRPTSession{Day: day.Format(tlsrptDay), PolicyDomain: domain, PolicyType: tlsrptNoPolicyFound}).Error; err != nil {
		t.Fatal(err)
	}
	count := func() int64 {
		var n int64
		tx.Model(&models.TLSRPTSession{}).Where("policy_domain = ?", domain).Count(&n)
		return n
	}
	w.sendTLSReports(day.Add(30 * time.Hour))
	if count() != 1 {
		t.Fatal("a refused report's sessions were dropped")
	}
	w.sendTLSReports(day.Add(73 * time.Hour))
	if count() != 0 {
		t.Fatal("a report was still tried two days after its day")
	}
}
