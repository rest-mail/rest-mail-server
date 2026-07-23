package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/pipeline"
	"gorm.io/gorm"
)

// seedTraceReadFixtures inserts one delivered trace (message_id set, continue
// outcome) and one rejected trace (message_id nil, dmarc_reject) and returns the
// handler bound to the transaction. Shared by the list-filter and per-message
// trace read tests.
func seedTraceReadFixtures(t *testing.T, tx *gorm.DB) *PipelineHandler {
	t.Helper()
	if err := tx.Exec("DELETE FROM message_traces").Error; err != nil {
		t.Fatalf("clear message_traces: %v", err)
	}

	mid := uint(1001)
	delivered := buildTrace(traceInputs{
		PipelineID: 5,
		Direction:  "inbound",
		Result: &pipeline.ExecutionResult{
			FinalAction: pipeline.ActionContinue,
			Duration:    2 * time.Millisecond,
			Steps: []pipeline.StepResult{
				{FilterName: "spf_check", Action: pipeline.ActionContinue},
				{FilterName: "dmarc_check", Action: pipeline.ActionContinue},
			},
		},
		Envelope:     pipeline.Envelope{MailFrom: "ok@remote.test", RcptTo: []string{"user@local.test"}, ClientIP: "203.0.113.1"},
		Transport:    "tls",
		RFCMessageID: "<delivered@remote.test>",
		Outcome:      outcomeDelivered,
		MessageID:    &mid,
	})
	rejected := buildTrace(traceInputs{
		PipelineID: 5,
		Direction:  "inbound",
		Result: &pipeline.ExecutionResult{
			FinalAction: pipeline.ActionReject,
			RejectMsg:   "DMARC policy=reject",
			Steps: []pipeline.StepResult{
				{FilterName: "dmarc_check", Action: pipeline.ActionReject, Log: pipeline.FilterLog{Result: "fail"}},
			},
		},
		Envelope:     pipeline.Envelope{MailFrom: "spoof@evil.test", RcptTo: []string{"user@local.test"}, ClientIP: "198.51.100.7"},
		Transport:    "plaintext",
		RFCMessageID: "<spoof@evil.test>",
		Outcome:      outcomeRejected,
	})
	if err := tx.Create(&delivered).Error; err != nil {
		t.Fatalf("persist delivered trace: %v", err)
	}
	if err := tx.Create(&rejected).Error; err != nil {
		t.Fatalf("persist rejected trace: %v", err)
	}
	return NewPipelineHandler(tx, nil)
}

// listTraces calls ListPipelineLogs with the given raw query string and decodes
// the trace list from the response.
func listTraces(t *testing.T, h *PipelineHandler, rawQuery string) []models.MessageTrace {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/pipelines/logs?"+rawQuery, nil)
	rr := httptest.NewRecorder()
	h.ListPipelineLogs(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("ListPipelineLogs(%q) status = %d, want 200 (body %s)", rawQuery, rr.Code, rr.Body.String())
	}
	var resp struct {
		Data []models.MessageTrace `json:"data"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	return resp.Data
}

// TestListPipelineLogs_TraceFilters proves the repointed handler reads
// message_traces and honours the outcome / reason_code / rfc_message_id / action
// filters, returning the richer trace shape.
func TestListPipelineLogs_TraceFilters(t *testing.T) {
	gdb := openTraceTestDB(t)
	tx := gdb.Begin()
	t.Cleanup(func() { tx.Rollback() })
	h := seedTraceReadFixtures(t, tx)

	// No filter → both traces.
	if got := listTraces(t, h, "limit=50"); len(got) != 2 {
		t.Fatalf("unfiltered list len = %d, want 2", len(got))
	}

	// outcome=rejected → only the rejected trace, with its derived reason_code
	// and trace-only PII surfaced (the richer shape).
	rej := listTraces(t, h, "outcome=rejected")
	if len(rej) != 1 || rej[0].Outcome != outcomeRejected {
		t.Fatalf("outcome=rejected → %+v, want one rejected", rej)
	}
	if rej[0].ReasonCode != string(pipeline.ReasonDMARCReject) {
		t.Errorf("rejected reason_code = %q, want %q", rej[0].ReasonCode, pipeline.ReasonDMARCReject)
	}
	if rej[0].MailFrom != "spoof@evil.test" || rej[0].ClientIP != "198.51.100.7" {
		t.Errorf("rejected trace PII = %q/%q, want spoof@evil.test/198.51.100.7", rej[0].MailFrom, rej[0].ClientIP)
	}
	if len(rej[0].Stages) == 0 {
		t.Error("rejected trace Stages empty, want the stage timeline JSON")
	}

	// reason_code filter.
	if got := listTraces(t, h, "reason_code=dmarc_reject"); len(got) != 1 || got[0].Outcome != outcomeRejected {
		t.Fatalf("reason_code=dmarc_reject → %+v, want the rejected trace", got)
	}

	// rfc_message_id filter → the delivered trace.
	del := listTraces(t, h, "rfc_message_id=%3Cdelivered@remote.test%3E")
	if len(del) != 1 || del[0].Outcome != outcomeDelivered {
		t.Fatalf("rfc_message_id filter → %+v, want the delivered trace", del)
	}
	if del[0].MessageID == nil || *del[0].MessageID != 1001 {
		t.Errorf("delivered MessageID = %v, want 1001", del[0].MessageID)
	}

	// action (=final_action) filter preserved from the legacy contract.
	if got := listTraces(t, h, "action=continue"); len(got) != 1 || got[0].Outcome != outcomeDelivered {
		t.Fatalf("action=continue → %+v, want the delivered (continue) trace", got)
	}
	if got := listTraces(t, h, "outcome=quarantined"); len(got) != 0 {
		t.Fatalf("outcome=quarantined → %+v, want none", got)
	}
}

// getMessageTrace calls GetMessageTrace with a chi-provided {id} param.
func getMessageTrace(h *PipelineHandler, id string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/messages/"+id+"/trace", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rr := httptest.NewRecorder()
	h.GetMessageTrace(rr, req)
	return rr
}

// TestGetMessageTrace_DeliveredAndMissing proves the per-message trace endpoint
// returns the delivered message's stage timeline and 404s an id with no
// delivered trace (non-delivered mail has a nil message_id, so it is
// unreachable here — correlate it via ListPipelineLogs + rfc_message_id).
func TestGetMessageTrace_DeliveredAndMissing(t *testing.T) {
	gdb := openTraceTestDB(t)
	tx := gdb.Begin()
	t.Cleanup(func() { tx.Rollback() })
	h := seedTraceReadFixtures(t, tx)

	// Delivered message → 200 with its stages.
	rr := getMessageTrace(h, "1001")
	if rr.Code != http.StatusOK {
		t.Fatalf("delivered trace status = %d, want 200 (body %s)", rr.Code, rr.Body.String())
	}
	var resp struct {
		Data models.MessageTrace `json:"data"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Data.Outcome != outcomeDelivered {
		t.Errorf("outcome = %q, want delivered", resp.Data.Outcome)
	}
	if resp.Data.MessageID == nil || *resp.Data.MessageID != 1001 {
		t.Errorf("MessageID = %v, want 1001", resp.Data.MessageID)
	}
	if len(resp.Data.Stages) == 0 {
		t.Error("Stages empty, want the ordered stage timeline")
	}

	// Unknown / non-delivered id → 404.
	if rr := getMessageTrace(h, "9999"); rr.Code != http.StatusNotFound {
		t.Fatalf("unknown id status = %d, want 404 (body %s)", rr.Code, rr.Body.String())
	}
	// Non-numeric id → 400.
	if rr := getMessageTrace(h, "abc"); rr.Code != http.StatusBadRequest {
		t.Fatalf("bad id status = %d, want 400", rr.Code)
	}
}
