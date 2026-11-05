package handlers

import (
	"errors"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/pipeline"
)

// Only the greylist or rate_limit step's own decision carries that reason: a fault in
// either, and any other step's deferral, is a plain deferral.
func TestDeferredErrorNamesOnlyAStepsOwnDecision(t *testing.T) {
	for _, test := range []struct {
		name string
		step pipeline.StepResult
		want error
	}{
		{"greylist decision", pipeline.StepResult{FilterName: "greylist", Action: pipeline.ActionDefer}, errGreylisted},
		{"greylist fault", pipeline.StepResult{FilterName: "greylist", Action: pipeline.ActionDefer, Error: "execute: database down"}, nil},
		{"rate limit decision", pipeline.StepResult{FilterName: "rate_limit", Action: pipeline.ActionDefer}, errRateLimited},
		{"rate limit fault", pipeline.StepResult{FilterName: "rate_limit", Action: pipeline.ActionDefer, Error: "execute: store down"}, nil},
		{"rspamd", pipeline.StepResult{FilterName: "rspamd", Action: pipeline.ActionDefer}, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := deferredError(&pipeline.ExecutionResult{FinalAction: pipeline.ActionDefer, Steps: []pipeline.StepResult{test.step}}, "try again later")
			for _, reason := range []error{errGreylisted, errRateLimited} {
				if got, want := errors.Is(err, reason), reason == test.want; got != want {
					t.Errorf("errors.Is(%v, %v) = %v, want %v", err, reason, got, want)
				}
			}
			if !strings.HasPrefix(err.Error(), "deferred:") {
				t.Errorf("%q is not a deferral to callers reading the prefix", err)
			}
		})
	}
}
