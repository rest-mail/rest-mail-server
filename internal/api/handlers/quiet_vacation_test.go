package handlers

import (
	"context"

	"github.com/rest-mail/rest-mail-server/internal/pipeline"
	"github.com/rest-mail/rest-mail-server/internal/pipeline/filters"
)

// quietVacation stands in for the vacation step every stored inbound pipeline runs
// (#772), in a test about something else: a registry that lacks it defers the mail.
type quietVacation struct{}

func (quietVacation) Name() string              { return "vacation" }
func (quietVacation) Type() pipeline.FilterType { return pipeline.FilterTypeAction }
func (quietVacation) Execute(context.Context, *pipeline.EmailJSON) (*pipeline.FilterResult, error) {
	return &pipeline.FilterResult{Type: pipeline.FilterTypeAction, Action: pipeline.ActionContinue}, nil
}

func withQuietVacation(registry *pipeline.Registry) *pipeline.Registry {
	registry.Register("dmarc_reports_received", filters.NewDMARCReportsReceived)
	registry.Register("vacation", func([]byte) (pipeline.Filter, error) { return quietVacation{}, nil })
	return registry
}
