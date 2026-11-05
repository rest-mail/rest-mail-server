package handlers

import (
	"errors"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/pipeline"
)

// Only the greylist step's own decision is greylisting: its fault and any other
// step's deferral are plain deferrals.
func TestDeferredErrorNamesOnlyAGreylistDecision(t *testing.T) {
	for _, test := range []struct {
		name string
		step pipeline.StepResult
		want bool
	}{
		{"greylist decision", pipeline.StepResult{FilterName: "greylist", Action: pipeline.ActionDefer}, true},
		{"greylist fault", pipeline.StepResult{FilterName: "greylist", Action: pipeline.ActionDefer, Error: "execute: database down"}, false},
		{"rate limit", pipeline.StepResult{FilterName: "rate_limit", Action: pipeline.ActionDefer}, false},
		{"rspamd", pipeline.StepResult{FilterName: "rspamd", Action: pipeline.ActionDefer}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := deferredError(&pipeline.ExecutionResult{FinalAction: pipeline.ActionDefer, Steps: []pipeline.StepResult{test.step}}, "try again later")
			if got := errors.Is(err, errGreylisted); got != test.want {
				t.Errorf("greylisted = %v, want %v: %v", got, test.want, err)
			}
			if !strings.HasPrefix(err.Error(), "deferred:") {
				t.Errorf("%q is not a deferral to callers reading the prefix", err)
			}
		})
	}
}
