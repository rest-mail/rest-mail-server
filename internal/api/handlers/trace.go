package handlers

import (
	"encoding/json"

	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/pipeline"
)

// traceRecorder is the async sink the delivery handlers hand a MessageTrace to.
// The concrete *trace.Recorder satisfies it. Kept as an interface so handlers
// depend on the narrow Record contract (and tests can capture traces without a
// running goroutine). Record must never block or fail message processing.
type traceRecorder interface {
	Record(models.MessageTrace)
}

// Bounded terminal-outcome vocabulary for MessageTrace.Outcome. This mirrors the
// restmail_pipeline_terminal metric domain plus the delivered/queued split that
// only the persisted trace distinguishes (the metric collapses both to their
// direction's happy path).
const (
	outcomeDelivered   = "delivered"   // inbound continue → Message row created
	outcomeQueued      = "queued"      // outbound continue → enqueued for delivery
	outcomeRejected    = "rejected"    // pipeline ActionReject
	outcomeQuarantined = "quarantined" // pipeline ActionQuarantine
	outcomeDiscarded   = "discarded"   // pipeline ActionDiscard
	outcomeDeferred    = "deferred"    // pipeline ActionDefer
)

// transportLabel maps an inbound-MX TLS flag to the bounded transport vocabulary
// (tls | plaintext | ""). A nil flag means transport is not applicable / unknown
// (a non inbound-MX delivery: IMAP APPEND, local webmail send, RESTMAIL) and is
// recorded as "" — never mislabelled plaintext.
func transportLabel(receivedTLS *bool) string {
	if receivedTLS == nil {
		return ""
	}
	if *receivedTLS {
		return "tls"
	}
	return "plaintext"
}

// terminalStep returns the step that produced a non-continue outcome — the last
// recorded step, since the engine breaks its loop immediately after recording a
// terminal action. Nil for a continue outcome (no reject step) or an empty run.
func terminalStep(result *pipeline.ExecutionResult) *pipeline.StepResult {
	if result == nil || result.FinalAction == pipeline.ActionContinue || len(result.Steps) == 0 {
		return nil
	}
	return &result.Steps[len(result.Steps)-1]
}

// traceInputs carries everything buildTrace needs from a terminal site. It keeps
// the mapping logic pure and unit-testable, independent of the request handler.
type traceInputs struct {
	PipelineID   uint
	Direction    string
	Result       *pipeline.ExecutionResult
	Envelope     pipeline.Envelope
	Transport    string // precomputed via transportLabel; tls|plaintext|""
	RFCMessageID string
	Outcome      string
	MessageID    *uint    // non-nil ONLY on the delivered path
	SpamScore    *float32 // nil until a spam filter surfaces a score (PR4/PR5)
}

// buildTrace assembles a durable MessageTrace from a pipeline execution and its
// terminal disposition. It derives:
//   - reason_code: pipeline.ReasonForStep on the terminal step (empty for a
//     continue outcome, which has no reject step);
//   - rcpt_to: the first envelope recipient (trace-only raw PII);
//   - stages: the []StepResult serialised as JSON;
//   - duration_ms / final_action: from the ExecutionResult.
//
// Sampled is always true in PR3 (capture-all); ExpiresAt is left nil (the PR4
// pruner assigns the retention horizon). message_id is passed through verbatim —
// the caller sets it non-nil only after a Message row exists.
func buildTrace(in traceInputs) models.MessageTrace {
	var rcpt string
	if len(in.Envelope.RcptTo) > 0 {
		rcpt = in.Envelope.RcptTo[0]
	}

	reason := ""
	if ts := terminalStep(in.Result); ts != nil {
		reason = string(pipeline.ReasonForStep(*ts))
	}

	var (
		finalAction string
		durationMS  int64
		stages      json.RawMessage
	)
	if in.Result != nil {
		finalAction = string(in.Result.FinalAction)
		durationMS = in.Result.Duration.Milliseconds()
		if b, err := json.Marshal(in.Result.Steps); err == nil {
			stages = b
		}
	}

	return models.MessageTrace{
		MessageID:    in.MessageID,
		RFCMessageID: in.RFCMessageID,
		Direction:    in.Direction,
		Transport:    in.Transport,
		MailFrom:     in.Envelope.MailFrom,
		RcptTo:       rcpt,
		ClientIP:     in.Envelope.ClientIP,
		PipelineID:   in.PipelineID,
		FinalAction:  finalAction,
		Outcome:      in.Outcome,
		ReasonCode:   reason,
		SpamScore:    in.SpamScore,
		DurationMS:   durationMS,
		Stages:       stages,
		Sampled:      true,
	}
}
