package handlers

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
	"github.com/rest-mail/rest-mail-server/internal/requestid"
)

type capturedTraces struct{ rows []models.MessageTrace }

func (c *capturedTraces) Record(t models.MessageTrace) { c.rows = append(c.rows, t) }

func requestTrace() models.MessageTrace {
	return buildTrace(traceInputs{
		PipelineID: 7, Direction: "inbound", Outcome: outcomeDelivered,
		Result:   &pipeline.ExecutionResult{FinalAction: pipeline.ActionContinue},
		Envelope: pipeline.Envelope{MailFrom: "sender@example.com", RcptTo: []string{"recipient@example.com"}},
	})
}

// A trace written while handling a request carries that request's ID, the one
// its gateway and API log lines carry (#1580). With no ID, or one that could not
// have been adopted, the column stays NULL.
func TestRecordTrace_CarriesTheRequestID(t *testing.T) {
	const id = "4f9c2d7e1a0b3c5d6e7f8091a2b3c4d5"
	for _, tc := range []struct {
		name string
		ctx  context.Context
		want string
	}{
		{"request", requestid.NewContext(context.Background(), id), id},
		{"no request", context.Background(), ""},
		{"invalid id", requestid.NewContext(context.Background(), "has space"), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			messages, restmail := &capturedTraces{}, &capturedTraces{}
			(&MessageHandler{recorder: messages}).recordTrace(tc.ctx, requestTrace())
			(&RestmailHandler{recorder: restmail}).recordTrace(tc.ctx, requestTrace())
			for name, got := range map[string]*capturedTraces{"messages": messages, "restmail": restmail} {
				if len(got.rows) != 1 {
					t.Fatalf("%s: recorded %d traces, want 1", name, len(got.rows))
				}
				switch rid := got.rows[0].RequestID; {
				case tc.want == "" && rid != nil:
					t.Errorf("%s: request_id = %q, want NULL", name, *rid)
				case tc.want != "" && (rid == nil || *rid != tc.want):
					t.Errorf("%s: request_id = %v, want %q", name, rid, tc.want)
				}
			}
		})
	}
}

// A send that buffers its traces until the transaction commits releases them
// with the ID of the request that made them, though the release has no request
// context of its own.
func TestRecordTrace_BufferedSendKeepsTheRequestID(t *testing.T) {
	const id = "gateway-session-1580"
	captured := &capturedTraces{}
	h := &MessageHandler{recorder: captured}
	effects := &draftSendEffects{handler: h}
	sender := *h
	sender.recorder = effects

	sender.recordTrace(requestid.NewContext(context.Background(), id), requestTrace())
	if len(captured.rows) != 0 {
		t.Fatalf("trace recorded before commit: %+v", captured.rows)
	}
	for _, release := range effects.pending {
		release()
	}
	if len(captured.rows) != 1 || captured.rows[0].RequestID == nil || *captured.rows[0].RequestID != id {
		t.Fatalf("released traces = %+v, want one with request_id %q", captured.rows, id)
	}
}

// The admin trace response names the request ID, and spells a missing one null.
func TestMessageTraceResponse_IncludesTheRequestID(t *testing.T) {
	id := "4f9c2d7e1a0b3c5d6e7f8091a2b3c4d5"
	for _, tc := range []struct {
		name string
		rid  *string
		want any
	}{{"recorded", &id, id}, {"none", nil, nil}} {
		row := requestTrace()
		row.RequestID = tc.rid
		out, err := messageTraceOf(row)
		if err != nil {
			t.Fatal(err)
		}
		b, err := json.Marshal(out)
		if err != nil {
			t.Fatal(err)
		}
		var body map[string]any
		if err := json.Unmarshal(b, &body); err != nil {
			t.Fatal(err)
		}
		got, ok := body["request_id"]
		if !ok || got != tc.want {
			t.Errorf("%s: request_id = %v (present %v), want %v", tc.name, got, ok, tc.want)
		}
	}
}

// The column round-trips through Postgres and reaches the admin response.
func TestMessageTrace_RequestIDRoundTripsTheDatabase(t *testing.T) {
	gdb := openTraceTestDB(t)
	tx := gdb.Begin()
	t.Cleanup(func() { tx.Rollback() })
	captured := &capturedTraces{}
	(&MessageHandler{recorder: captured}).recordTrace(requestid.NewContext(context.Background(), "4f9c2d7e1a0b3c5d6e7f8091a2b3c4d5"), requestTrace())
	withID, without := captured.rows[0], requestTrace()
	if err := tx.Create(&withID).Error; err != nil {
		t.Fatalf("insert trace with request_id: %v", err)
	}
	if err := tx.Create(&without).Error; err != nil {
		t.Fatalf("insert trace without request_id: %v", err)
	}
	var got models.MessageTrace
	if err := tx.First(&got, withID.ID).Error; err != nil {
		t.Fatal(err)
	}
	out, err := messageTraceOf(got)
	if err != nil {
		t.Fatal(err)
	}
	if out.RequestID == nil || *out.RequestID != "4f9c2d7e1a0b3c5d6e7f8091a2b3c4d5" {
		t.Fatalf("stored request_id = %v", out.RequestID)
	}
	var isNull bool
	if err := tx.Raw("SELECT request_id IS NULL FROM message_traces WHERE id = ?", without.ID).Scan(&isNull).Error; err != nil {
		t.Fatal(err)
	}
	if !isNull {
		t.Fatal("a trace written outside a request stored a request_id, want NULL")
	}
}
