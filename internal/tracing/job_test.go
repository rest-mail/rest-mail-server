package tracing_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/codes"

	"github.com/rest-mail/rest-mail-server/internal/requestid"
	"github.com/rest-mail/rest-mail-server/internal/tracing"
	"github.com/rest-mail/rest-mail-server/internal/tracing/tracingtest"
)

// A background job is a trace of its own (#1580): its root span starts a new
// trace even under a request's span, and carries a job ID that its children
// and its context-logged lines carry too.
func TestJob_RootSpanWithAJobIDItsLogsCarry(t *testing.T) {
	rec := tracingtest.Record(t)
	var logs bytes.Buffer
	log := slog.New(requestid.NewHandler(slog.NewJSONHandler(&logs, nil)))

	parent, parentSpan := tracing.Start(requestid.NewContext(context.Background(), requestid.New()), "request")
	failed := errors.New("someone@example.test refused")
	err := tracing.Job(parent, "job.test", func(ctx context.Context) error {
		_, child := tracing.Start(ctx, "job.test.step")
		child.End()
		log.InfoContext(ctx, "job ran")
		return failed
	})
	parentSpan.End()
	if !errors.Is(err, failed) {
		t.Fatalf("Job returned %v", err)
	}

	job := tracingtest.WaitFor(t, rec, "job.test")
	step := tracingtest.WaitFor(t, rec, "job.test.step")
	request := tracingtest.WaitFor(t, rec, "request")
	if job.Parent().IsValid() {
		t.Error("the job's span has a parent; want a new root")
	}
	if job.SpanContext().TraceID() == request.SpanContext().TraceID() {
		t.Error("the job joined the request's trace")
	}
	if step.Parent().SpanID() != job.SpanContext().SpanID() {
		t.Error("the step is not under the job's span")
	}
	id := tracingtest.Attr(job, requestid.Attr)
	if !strings.HasPrefix(id, tracing.JobPrefix) || !requestid.Valid(id) {
		t.Fatalf("job ID = %q", id)
	}
	if got := tracingtest.Attr(step, requestid.Attr); got != id {
		t.Errorf("step's ID = %q, want the job's %q", got, id)
	}
	if got := tracingtest.Attr(job, "job.name"); got != "job.test" {
		t.Errorf("job.name = %q", got)
	}
	if !strings.Contains(logs.String(), `"request_id":"`+id+`"`) {
		t.Errorf("the job's log line lacks its ID %q: %s", id, logs.String())
	}
	if job.Status().Code != codes.Error || tracingtest.Attr(job, "error.type") != "*errors.errorString" {
		t.Errorf("status = %v, error.type = %q", job.Status(), tracingtest.Attr(job, "error.type"))
	}
	for _, kv := range job.Attributes() {
		if strings.Contains(kv.Value.String(), "@") {
			t.Errorf("attribute %s = %q holds an address", kv.Key, kv.Value.String())
		}
	}
}

// With tracing off a job's span is a no-op one: nothing records, nothing is
// exported, and the job still gets its ID for its log lines.
func TestJob_OffIsNoop(t *testing.T) {
	ctx, span := tracing.StartJob(context.Background(), "job.test")
	defer span.End()
	if span.IsRecording() || span.SpanContext().IsValid() {
		t.Fatal("a job span recorded with tracing off")
	}
	if id := requestid.FromContext(ctx); !strings.HasPrefix(id, tracing.JobPrefix) {
		t.Fatalf("job ID = %q", id)
	}
}
