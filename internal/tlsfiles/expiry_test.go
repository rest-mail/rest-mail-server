package tlsfiles

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/rest-mail/rest-mail-server/internal/mtls/mtlstest"
)

const expiryMetric = "restmail_tls_cert_expiry_timestamp_seconds"

// publishPair writes a pair expiring at notAfter the way a certificate manager
// does, key first, each file renamed into place.
func publishPair(t *testing.T, certPath, keyPath string, notAfter time.Time) {
	t.Helper()
	ca, err := mtlstest.NewCA("expiry test CA")
	if err != nil {
		t.Fatal(err)
	}
	certPEM, keyPEM, err := ca.IssueServer("mail.test", []string{"mail.test"}, nil, time.Now().Add(-time.Hour), notAfter)
	if err != nil {
		t.Fatal(err)
	}
	replace(t, keyPath, keyPEM)
	replace(t, certPath, certPEM)
}

func pairPaths(t *testing.T) (certPath, keyPath string) {
	t.Helper()
	dir := t.TempDir()
	return filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
}

// scrapeExpiry gathers c the way /metrics does and returns the series, with
// false when it is absent. Each collector here is private to its test, so
// loaders other tests left behind cannot move the value.
func scrapeExpiry(t *testing.T, c *expiryCollector) (int64, bool) {
	t.Helper()
	registry := prometheus.NewPedanticRegistry()
	registry.MustRegister(c)
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() != expiryMetric {
			continue
		}
		if n := len(family.GetMetric()); n != 1 {
			t.Fatalf("%s has %d series, want one", expiryMetric, n)
		}
		metric := family.GetMetric()[0]
		if len(metric.GetLabel()) != 0 {
			t.Fatalf("%s has labels %v, want none", expiryMetric, metric.GetLabel())
		}
		return int64(metric.GetGauge().GetValue()), true
	}
	return 0, false
}

func wantExpiry(t *testing.T, c *expiryCollector, want time.Time) {
	t.Helper()
	got, ok := scrapeExpiry(t, c)
	if !ok {
		t.Fatalf("%s is absent, want %d", expiryMetric, want.Unix())
	}
	if got != want.Unix() {
		t.Fatalf("%s = %d, want %d (%s)", expiryMetric, got, want.Unix(), want.UTC())
	}
}

// The series is the soonest expiry among the pairs this process loaded, and it
// is absent until one has loaded: an unknown expiry must not read as expired.
func TestExpiryReportsTheSoonestLoadedPair(t *testing.T) {
	c := newExpiryCollector()
	if value, ok := scrapeExpiry(t, c); ok {
		t.Fatalf("%s = %d with no pair loaded, want it absent", expiryMetric, value)
	}

	unusedCert, unusedKey := pairPaths(t)
	if _, err := newLoader(unusedCert, unusedKey, 0, c); err == nil {
		t.Fatal("a missing pair loaded")
	}
	if value, ok := scrapeExpiry(t, c); ok {
		t.Fatalf("%s = %d after only a failed load, want it absent", expiryMetric, value)
	}
	// Each unknown SNI name is a failed load, so none of them may be kept.
	if n := len(c.loaders); n != 0 {
		t.Fatalf("%d loaders tracked after only a failed load, want 0", n)
	}

	base := time.Now().Add(90 * 24 * time.Hour).Truncate(time.Second)
	laterCert, laterKey := pairPaths(t)
	publishPair(t, laterCert, laterKey, base.Add(24*time.Hour))
	if _, err := newLoader(laterCert, laterKey, 0, c); err != nil {
		t.Fatal(err)
	}
	wantExpiry(t, c, base.Add(24*time.Hour))

	soonerCert, soonerKey := pairPaths(t)
	publishPair(t, soonerCert, soonerKey, base)
	if _, err := newLoader(soonerCert, soonerKey, 0, c); err != nil {
		t.Fatal(err)
	}
	wantExpiry(t, c, base)
}

// A listener nobody connects to must not raise a false alarm for a certificate
// its manager already renewed: a scrape alone picks the new pair up, and that
// is then the pair the next handshake gets.
func TestExpiryFollowsARenewalWithoutAHandshake(t *testing.T) {
	c := newExpiryCollector()
	certPath, keyPath := pairPaths(t)
	first := time.Now().Add(10 * 24 * time.Hour).Truncate(time.Second)
	publishPair(t, certPath, keyPath, first)
	l, err := newLoader(certPath, keyPath, 0, c)
	if err != nil {
		t.Fatal(err)
	}
	wantExpiry(t, c, first)

	renewed := first.Add(80 * 24 * time.Hour)
	publishPair(t, certPath, keyPath, renewed)
	wantExpiry(t, c, renewed)
	if got := l.cert.Leaf.NotAfter; !got.Equal(renewed) {
		t.Fatalf("the loader holds a pair expiring %s after the scrape, want %s", got, renewed)
	}
	if n := len(c.loaders); n != 1 {
		t.Fatalf("%d loaders tracked after a renewal, want the one", n)
	}
}

// A renewal that failed leaves the old certificate serving, so the series keeps
// that certificate's expiry. Scrapes detect and clear the failure even when
// the listener is idle, and checks during the same failure count it only once.
func TestExpiryStaysOnTheServedPairWhenARenewalBreaks(t *testing.T) {
	c := newExpiryCollector()
	certPath, keyPath := pairPaths(t)
	served := time.Now().Add(10 * 24 * time.Hour).Truncate(time.Second)
	publishPair(t, certPath, keyPath, served)
	l, err := newLoader(certPath, keyPath, 0, c)
	if err != nil {
		t.Fatal(err)
	}
	failures := testutil.ToFloat64(reloadFailures)
	failing := testutil.ToFloat64(reloadFailing)

	replace(t, certPath, []byte("broken renewal"))
	wantExpiry(t, c, served)
	if got := testutil.ToFloat64(reloadFailing) - failing; got != 1 {
		t.Fatalf("a scrape moved the failing gauge by %v, want 1", got)
	}
	if got := testutil.ToFloat64(reloadFailures) - failures; got != 1 {
		t.Fatalf("a scrape counted %v reload failures, want 1", got)
	}

	if l.Current() == nil {
		t.Fatal("the broken renewal displaced the served pair")
	}
	wantExpiry(t, c, served)
	if got := testutil.ToFloat64(reloadFailing) - failing; got != 1 {
		t.Fatalf("repeated checks moved the failing gauge by %v, want 1", got)
	}
	if got := testutil.ToFloat64(reloadFailures) - failures; got != 1 {
		t.Fatalf("repeated checks counted %v reload failures, want 1", got)
	}

	repaired := served.Add(80 * 24 * time.Hour)
	publishPair(t, certPath, keyPath, repaired)
	wantExpiry(t, c, repaired)
	if got := testutil.ToFloat64(reloadFailing) - failing; got != 0 {
		t.Fatalf("the failing gauge stayed %v above its baseline after the repair", got)
	}
	if got := testutil.ToFloat64(reloadFailures) - failures; got != 1 {
		t.Fatalf("recovery changed the failure count to %v, want 1", got)
	}
}

func TestReloadCountsDistinctBrokenVersionsAcrossChecks(t *testing.T) {
	c := newExpiryCollector()
	certPath, keyPath := pairPaths(t)
	expiry := time.Now().Add(24 * time.Hour).Truncate(time.Second)
	publishPair(t, certPath, keyPath, expiry)
	l, err := newLoader(certPath, keyPath, 0, c)
	if err != nil {
		t.Fatal(err)
	}
	good := l.Current()
	failures := testutil.ToFloat64(reloadFailures)
	failing := testutil.ToFloat64(reloadFailing)
	wantMetrics := func(count, active float64) {
		t.Helper()
		if got := testutil.ToFloat64(reloadFailures) - failures; got != count {
			t.Errorf("failure counter change=%v, want %v", got, count)
		}
		if got := testutil.ToFloat64(reloadFailing) - failing; got != active {
			t.Errorf("failing gauge change=%v, want %v", got, active)
		}
	}
	handshake := func() {
		t.Helper()
		if got, err := l.GetClientCertificate(nil); got != good || err != nil {
			t.Fatalf("handshake after failed renewal: got pair %p, want %p, error=%v", got, good, err)
		}
	}

	if err := os.Rename(certPath, certPath+".good"); err != nil {
		t.Fatal(err)
	}
	wantExpiry(t, c, expiry)
	wantMetrics(1, 1)
	handshake()
	wantMetrics(1, 1)

	replace(t, certPath, []byte("invalid first"))
	handshake()
	wantMetrics(2, 1)
	wantExpiry(t, c, expiry)
	wantMetrics(2, 1)

	// A new file version with the same parser error is another failed renewal.
	replace(t, certPath, []byte("invalid again"))
	wantExpiry(t, c, expiry)
	wantMetrics(3, 1)

	if err := os.Rename(certPath+".good", certPath); err != nil {
		t.Fatal(err)
	}
	handshake()
	wantMetrics(3, 0)
	if err := os.Rename(certPath, certPath+".good"); err != nil {
		t.Fatal(err)
	}
	handshake()
	wantMetrics(4, 1)
	if err := os.Rename(certPath+".good", certPath); err != nil {
		t.Fatal(err)
	}
	wantExpiry(t, c, expiry)
	wantMetrics(4, 0)
}

// Scrapes share the bound on filesystem work that handshakes have, including
// while the replacement on disk does not load.
func TestExpiryChecksAreBoundedByTheCheckInterval(t *testing.T) {
	c := newExpiryCollector()
	certPath, keyPath := pairPaths(t)
	served := time.Now().Add(10 * 24 * time.Hour).Truncate(time.Second)
	publishPair(t, certPath, keyPath, served)
	l, err := newLoader(certPath, keyPath, CheckInterval, c)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	l.now = func() time.Time { return now }
	l.nextCheck = now.Add(CheckInterval)

	renewed := served.Add(80 * 24 * time.Hour)
	publishPair(t, certPath, keyPath, renewed)
	wantExpiry(t, c, served)

	now = now.Add(CheckInterval)
	replace(t, certPath, []byte("broken renewal"))
	wantExpiry(t, c, served)

	// This pair is good, but the failed check above used up the interval.
	publishPair(t, certPath, keyPath, renewed)
	wantExpiry(t, c, served)

	now = now.Add(CheckInterval)
	wantExpiry(t, c, renewed)
}
