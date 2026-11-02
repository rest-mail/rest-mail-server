package handlers

import (
	"errors"

	"github.com/rest-mail/rest-mail-server/internal/pipeline"
)

// deliveryStage is the part of a local delivery to run (#1437).
type deliveryStage int

const (
	// deliverWhole runs the whole pipeline and stores the message, while the caller waits.
	deliverWhole deliveryStage = iota
	// deliverRequired runs only the required checks and stores nothing, so a refusal can be
	// the sender's answer.
	deliverRequired
	// deliverContent runs the content checks and stores the message. The sender has
	// already been answered.
	deliverContent
)

// errContentStageNext says the required checks passed. It is not a failure: the message is
// accepted, and has yet to run its content stage. A nil answer with no error from
// deliverRequired means the message was dealt with already, as a DMARC quarantine is.
var errContentStageNext = errors.New("accepted: the content checks come next")

// deliveryState is what the required stage leaves for the content stage, stored with the queued
// message: its verdicts and skips, and what its filters added to the message, which parsing the
// stored bytes again would not bring back.
type deliveryState struct {
	Run        pipeline.RunState   `json:"run"`
	RawHeaders map[string][]string `json:"raw_headers,omitempty"`
	Metadata   map[string]string   `json:"metadata,omitempty"`
}

func newDeliveryState() *deliveryState {
	return &deliveryState{Run: *pipeline.NewRunState()}
}

// keep records what the required stage's filters did to the message.
func (s *deliveryState) keep(email *pipeline.EmailJSON) {
	s.RawHeaders, s.Metadata = email.Headers.Raw, email.Metadata
}

// stageOf is the part of the pipeline this delivery runs, and the state it runs with. For the
// content stage it first puts back what the required stage added to the message.
func (p localDeliveryParams) stageOf(cfg *pipeline.PipelineConfig, email *pipeline.EmailJSON) (*pipeline.PipelineConfig, *pipeline.RunState) {
	switch p.Stage {
	case deliverRequired:
		required, _ := pipeline.Split(cfg)
		return required, &p.State.Run
	case deliverContent:
		_, content := pipeline.Split(cfg)
		if p.State.RawHeaders != nil {
			email.Headers.Raw = p.State.RawHeaders
		}
		for key, value := range p.State.Metadata {
			if email.Metadata == nil {
				email.Metadata = map[string]string{}
			}
			email.Metadata[key] = value
		}
		return content, &p.State.Run
	case deliverWhole:
	}
	return cfg, pipeline.NewRunState()
}
