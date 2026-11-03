package connlimiter

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/rest-mail/rest-mail-server/internal/metrics"
)

// TestConnectionsAccepted: tagging a limiter creates its protocol's
// restmail_connections_accepted_total series at 0, so SMTPConnectionsStopped
// and IMAPConnectionsStopped see a gateway that has admitted nothing yet;
// each admitted connection adds one, a refused one and a release nothing.
func TestConnectionsAccepted(t *testing.T) {
	const proto = "connlimiter-accepted-test"
	series := `restmail_connections_accepted_total{protocol="` + proto + `"}`
	before := testutil.CollectAndCount(metrics.ConnectionsAccepted)
	l := New(Config{MaxPerIP: 1, MaxGlobal: 10})
	l.SetProtocol(proto)
	if got := testutil.CollectAndCount(metrics.ConnectionsAccepted); got != before+1 {
		t.Fatalf("SetProtocol left %d accepted-connection series, want %d (its own, at 0)", got, before+1)
	}
	ctr := metrics.ConnectionsAccepted.WithLabelValues(proto)
	if got := testutil.ToFloat64(ctr); got != 0 {
		t.Fatalf("%s = %v before any connection, want 0", series, got)
	}

	if !l.Accept("1.2.3.4") {
		t.Fatal("expected Accept to succeed")
	}
	if l.Accept("1.2.3.4") {
		t.Fatal("expected the per-IP limit to refuse a second connection")
	}
	l.Release("1.2.3.4")
	if !l.Accept("1.2.3.4") {
		t.Fatal("expected Accept to succeed after release")
	}
	if got := testutil.ToFloat64(ctr); got != 2 {
		t.Errorf("%s = %v, want 2 (two admitted, one refused)", series, got)
	}
}
