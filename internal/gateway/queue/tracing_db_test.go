package queue

import (
	"strings"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/rest-mail/rest-mail-server/internal/requestid"
	"github.com/rest-mail/rest-mail-server/internal/tracing"
	"github.com/rest-mail/rest-mail-server/internal/tracing/tracingtest"
)

// One delivery attempt is one trace of its own (#1580): a queue.deliver root
// span with the MX lookup, the RESTMAIL probe and the SMTP attempt under it,
// its transport, status and attempt number on it, a job ID on every span, and
// no address anywhere.
func TestDeliveryAttempt_IsOneRootSpanWithItsSteps(t *testing.T) {
	rec := tracingtest.Record(t)
	db := openResetWorkerDB(t)
	migrateDeliveryTables(t, db)
	_, sender := sendingDomain(t, db)
	mx := newTimedMX(t, timedMXOptions{})
	item := queueTimedRow(t, db, sender.Address, nil)

	claimingWorker(t, db, mx, false).processOne(0)
	if row := readRow(t, db, item.ID); row.Status != "delivered" {
		t.Fatalf("status = %s (%s), want delivered", row.Status, row.LastError)
	}

	root := tracingtest.WaitFor(t, rec, "queue.deliver")
	if root.Parent().IsValid() {
		t.Error("queue.deliver has a parent; want a root span")
	}
	for key, want := range map[string]string{
		"queue.transport": "smtp",
		"queue.status":    "delivered",
		"queue.attempt":   "1",
		"job.name":        "queue.deliver",
	} {
		if got := tracingtest.Attr(root, key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	id := tracingtest.Attr(root, requestid.Attr)
	if !strings.HasPrefix(id, tracing.JobPrefix) || !requestid.Valid(id) {
		t.Fatalf("job ID = %q", id)
	}

	children := map[string]sdktrace.ReadOnlySpan{}
	for _, s := range rec.Ended() {
		if s.Parent().SpanID() == root.SpanContext().SpanID() {
			children[s.Name()] = s
		}
	}
	for _, name := range []string{"queue.mx_lookup", "queue.restmail", "queue.smtp"} {
		s, ok := children[name]
		if !ok {
			t.Errorf("no %s span under queue.deliver; children: %v", name, children)
			continue
		}
		if got := tracingtest.Attr(s, requestid.Attr); got != id {
			t.Errorf("%s job ID = %q, want %q", name, got, id)
		}
	}
	if got := tracingtest.Attr(children["queue.restmail"], "queue.restmail.upgraded"); got != "false" {
		t.Errorf("queue.restmail.upgraded = %q, want false", got)
	}
	if got := tracingtest.Attr(children["queue.mx_lookup"], "queue.mx_count"); got != "1" {
		t.Errorf("queue.mx_count = %q", got)
	}

	for _, s := range rec.Ended() {
		if strings.Contains(s.Name(), "@") {
			t.Errorf("span name %q holds an address", s.Name())
		}
		for _, kv := range s.Attributes() {
			if strings.Contains(kv.Value.String(), "@") {
				t.Errorf("span %q attribute %s = %q holds an address", s.Name(), kv.Key, kv.Value.String())
			}
		}
	}
}
