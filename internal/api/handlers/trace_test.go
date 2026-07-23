package handlers

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/pipeline"
)

// TestTransportLabel pins the tls|plaintext|"" derivation: a nil flag (non
// inbound-MX / no transport info) is "", never mislabelled plaintext.
func TestTransportLabel(t *testing.T) {
	tru, fls := true, false
	cases := []struct {
		name string
		in   *bool
		want string
	}{
		{"nil (unknown / not applicable)", nil, ""},
		{"tls", &tru, "tls"},
		{"plaintext", &fls, "plaintext"},
	}
	for _, c := range cases {
		if got := transportLabel(c.in); got != c.want {
			t.Errorf("%s: transportLabel = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestTerminalStep returns the last step for a non-continue outcome and nil for
// a continue outcome or an empty run.
func TestTerminalStep(t *testing.T) {
	steps := []pipeline.StepResult{
		{FilterName: "spf_check", Action: pipeline.ActionContinue},
		{FilterName: "dmarc_check", Action: pipeline.ActionReject},
	}
	rejected := &pipeline.ExecutionResult{FinalAction: pipeline.ActionReject, Steps: steps}
	if ts := terminalStep(rejected); ts == nil || ts.FilterName != "dmarc_check" {
		t.Errorf("terminalStep(rejected) = %+v, want dmarc_check step", ts)
	}

	cont := &pipeline.ExecutionResult{FinalAction: pipeline.ActionContinue, Steps: steps}
	if ts := terminalStep(cont); ts != nil {
		t.Errorf("terminalStep(continue) = %+v, want nil", ts)
	}

	if ts := terminalStep(&pipeline.ExecutionResult{FinalAction: pipeline.ActionReject}); ts != nil {
		t.Errorf("terminalStep(empty) = %+v, want nil", ts)
	}
	if ts := terminalStep(nil); ts != nil {
		t.Errorf("terminalStep(nil) = %+v, want nil", ts)
	}
}

// TestBuildTrace_Delivered checks the happy path: outcome delivered, non-nil
// message_id, empty reason_code (continue has no reject step), and the envelope
// PII / transport / stages all mapped through.
func TestBuildTrace_Delivered(t *testing.T) {
	mid := uint(42)
	res := &pipeline.ExecutionResult{
		FinalAction: pipeline.ActionContinue,
		Duration:    3 * time.Millisecond,
		Steps: []pipeline.StepResult{
			{FilterName: "spf_check", Action: pipeline.ActionContinue},
			{FilterName: "dmarc_check", Action: pipeline.ActionContinue},
		},
	}
	tr := buildTrace(traceInputs{
		PipelineID: 7,
		Direction:  "inbound",
		Result:     res,
		Envelope: pipeline.Envelope{
			MailFrom: "sender@remote.test",
			RcptTo:   []string{"user@local.test", "other@local.test"},
			ClientIP: "203.0.113.9",
		},
		Transport:    "tls",
		RFCMessageID: "<abc@remote.test>",
		Outcome:      outcomeDelivered,
		MessageID:    &mid,
	})

	if tr.Outcome != outcomeDelivered {
		t.Errorf("Outcome = %q, want delivered", tr.Outcome)
	}
	if tr.MessageID == nil || *tr.MessageID != 42 {
		t.Errorf("MessageID = %v, want 42", tr.MessageID)
	}
	if tr.ReasonCode != "" {
		t.Errorf("ReasonCode = %q, want empty for a delivered (continue) outcome", tr.ReasonCode)
	}
	if tr.FinalAction != string(pipeline.ActionContinue) {
		t.Errorf("FinalAction = %q, want continue", tr.FinalAction)
	}
	if tr.Transport != "tls" {
		t.Errorf("Transport = %q, want tls", tr.Transport)
	}
	if tr.MailFrom != "sender@remote.test" || tr.ClientIP != "203.0.113.9" {
		t.Errorf("PII mismatch: mail_from=%q client_ip=%q", tr.MailFrom, tr.ClientIP)
	}
	if tr.RcptTo != "user@local.test" {
		t.Errorf("RcptTo = %q, want first recipient user@local.test", tr.RcptTo)
	}
	if tr.RFCMessageID != "<abc@remote.test>" {
		t.Errorf("RFCMessageID = %q", tr.RFCMessageID)
	}
	if tr.DurationMS != 3 {
		t.Errorf("DurationMS = %d, want 3", tr.DurationMS)
	}
	if !tr.Sampled {
		t.Error("Sampled = false, want true (PR3 captures all)")
	}
	if tr.ExpiresAt != nil {
		t.Errorf("ExpiresAt = %v, want nil (horizon assigned by PR4)", tr.ExpiresAt)
	}
	// Stages must round-trip the []StepResult as JSON.
	var stages []pipeline.StepResult
	if err := json.Unmarshal(tr.Stages, &stages); err != nil {
		t.Fatalf("stages not valid JSON: %v", err)
	}
	if len(stages) != 2 {
		t.Errorf("stages len = %d, want 2", len(stages))
	}
}

// TestBuildTrace_Rejected checks a DMARC reject terminal: outcome rejected, nil
// message_id, and reason_code derived via pipeline.ReasonForStep on the terminal
// step (dmarc_check + reject → dmarc_reject).
func TestBuildTrace_Rejected(t *testing.T) {
	res := &pipeline.ExecutionResult{
		FinalAction: pipeline.ActionReject,
		RejectMsg:   "DMARC policy",
		Duration:    time.Millisecond,
		Steps: []pipeline.StepResult{
			{FilterName: "spf_check", Action: pipeline.ActionContinue},
			{FilterName: "dmarc_check", Action: pipeline.ActionReject, Log: pipeline.FilterLog{Result: "fail"}},
		},
	}
	tr := buildTrace(traceInputs{
		PipelineID:   7,
		Direction:    "inbound",
		Result:       res,
		Envelope:     pipeline.Envelope{MailFrom: "spoof@evil.test", RcptTo: []string{"user@local.test"}},
		RFCMessageID: "<spoof@evil.test>",
		Outcome:      outcomeRejected,
	})

	if tr.Outcome != outcomeRejected {
		t.Errorf("Outcome = %q, want rejected", tr.Outcome)
	}
	if tr.MessageID != nil {
		t.Errorf("MessageID = %v, want nil for a rejected (non-delivered) trace", tr.MessageID)
	}
	if tr.ReasonCode != string(pipeline.ReasonDMARCReject) {
		t.Errorf("ReasonCode = %q, want %q", tr.ReasonCode, pipeline.ReasonDMARCReject)
	}
	// Correlation for non-delivered mail is via rfc_message_id.
	if tr.RFCMessageID != "<spoof@evil.test>" {
		t.Errorf("RFCMessageID = %q", tr.RFCMessageID)
	}
}

// TestBuildTrace_Quarantined checks a DMARC quarantine terminal maps to the
// dmarc_quarantine reason with a nil message_id.
func TestBuildTrace_Quarantined(t *testing.T) {
	res := &pipeline.ExecutionResult{
		FinalAction: pipeline.ActionQuarantine,
		Steps: []pipeline.StepResult{
			{FilterName: "dmarc_check", Action: pipeline.ActionQuarantine, Log: pipeline.FilterLog{Result: "fail"}},
		},
	}
	tr := buildTrace(traceInputs{
		Direction: "inbound",
		Result:    res,
		Envelope:  pipeline.Envelope{MailFrom: "x@remote.test", RcptTo: []string{"user@local.test"}},
		Outcome:   outcomeQuarantined,
	})

	if tr.Outcome != outcomeQuarantined {
		t.Errorf("Outcome = %q, want quarantined", tr.Outcome)
	}
	if tr.MessageID != nil {
		t.Errorf("MessageID = %v, want nil", tr.MessageID)
	}
	if tr.ReasonCode != string(pipeline.ReasonDMARCQuarantine) {
		t.Errorf("ReasonCode = %q, want %q", tr.ReasonCode, pipeline.ReasonDMARCQuarantine)
	}
	if tr.Transport != "" {
		t.Errorf("Transport = %q, want empty when unset", tr.Transport)
	}
}

// TestBuildTrace_NilResult tolerates a nil ExecutionResult (defensive): no
// panic, empty stages/final_action, zero duration.
func TestBuildTrace_NilResult(t *testing.T) {
	tr := buildTrace(traceInputs{Direction: "inbound", Outcome: outcomeDeferred})
	if tr.FinalAction != "" || tr.DurationMS != 0 || tr.Stages != nil {
		t.Errorf("nil-result trace not empty: %+v", tr)
	}
	if tr.Outcome != outcomeDeferred {
		t.Errorf("Outcome = %q, want deferred", tr.Outcome)
	}
}
