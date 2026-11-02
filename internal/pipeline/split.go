package pipeline

// RunState is what one run of a pipeline has decided so far, and it survives being stored with
// a queued message: the authentication verdicts, and the filters an earlier one said to skip.
type RunState struct {
	Auth []AuthResult    `json:"auth,omitempty"`
	Skip map[string]bool `json:"skip,omitempty"`
}

// NewRunState is the state a run starts with: no verdicts, and nothing to skip.
func NewRunState() *RunState {
	return &RunState{Skip: map[string]bool{}}
}

// requiredChecks are the filters that decide whether a message is accepted at all, so they run
// while the sender waits and can be refused or deferred (#1437). Everything else, a filter added
// later included, is a content check: it runs after the sender has been answered, and what it
// finds is for the mailbox owner, never the sender.
//
// The allowlists are here because they tell later filters to skip, including required ones.
var requiredChecks = map[string]bool{
	"size_check":        true,
	"header_validate":   true,
	"recipient_check":   true,
	"sender_verify":     true,
	"rate_limit":        true,
	"spf_check":         true,
	"dkim_verify":       true,
	"arc_verify":        true,
	"dmarc_check":       true,
	"domain_allowlist":  true,
	"contact_whitelist": true,
	"greylist":          true,
}

// Split divides a pipeline into its required checks and its content checks, each keeping the
// order it had. Run them with ExecuteStage and one RunState, required first.
func Split(pipeline *PipelineConfig) (required, content *PipelineConfig) {
	first, second := *pipeline, *pipeline
	first.Filters, second.Filters = nil, nil
	for _, filter := range pipeline.Filters {
		if requiredChecks[filter.Name] {
			first.Filters = append(first.Filters, filter)
		} else {
			second.Filters = append(second.Filters, filter)
		}
	}
	return &first, &second
}
