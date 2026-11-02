package pipeline

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
)

func names(p *PipelineConfig) []string {
	out := []string{}
	for _, f := range p.Filters {
		out = append(out, f.Name)
	}
	return out
}

func equalNames(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestSplitKeepsOrderAndSendsTheUnknownToContent(t *testing.T) {
	pipeline := &PipelineConfig{Direction: "inbound", Filters: []FilterConfig{
		{Name: "size_check"}, {Name: "rspamd"}, {Name: "spf_check"}, {Name: "sieve"}, {Name: "dmarc_check"}, {Name: "something_new"},
	}}
	required, content := Split(pipeline)
	if got, want := names(required), []string{"size_check", "spf_check", "dmarc_check"}; !equalNames(got, want) {
		t.Errorf("required %v, want %v", got, want)
	}
	if got, want := names(content), []string{"rspamd", "sieve", "something_new"}; !equalNames(got, want) {
		t.Errorf("content %v, want %v", got, want)
	}
	if len(pipeline.Filters) != 6 {
		t.Error("the pipeline itself was changed")
	}
	if required.Direction != "inbound" || content.Direction != "inbound" {
		t.Error("the parts lost the pipeline's settings")
	}
}

// The default inbound pipeline's required checks come first, so splitting it changes the order
// of nothing. A default that puts a content check first shows up here.
func TestSplitLeavesTheDefaultPipelineInOrder(t *testing.T) {
	seenContent := false
	for _, filter := range defaultInboundFilters() {
		if !requiredChecks[filter.Name] {
			seenContent = true
		} else if seenContent {
			t.Errorf("required check %s runs after a content check", filter.Name)
		}
	}
}

// stageFilter notes what ran and whether a verdict was visible, and records one as dkim_verify.
type stageFilter struct {
	name    string
	result  *FilterResult
	ran     *[]string
	sawDKIM *bool
}

func (f *stageFilter) Name() string     { return f.name }
func (f *stageFilter) Type() FilterType { return FilterTypeAction }
func (f *stageFilter) Execute(ctx context.Context, _ *EmailJSON) (*FilterResult, error) {
	*f.ran = append(*f.ran, f.name)
	if f.sawDKIM != nil {
		*f.sawDKIM = len(AuthResultsFromContext(ctx).List()) != 0
	}
	if f.name == "dkim_verify" {
		AuthResultsFromContext(ctx).Add(AuthResult{Method: AuthDKIM, Result: "pass"})
	}
	return f.result, nil
}

// The two parts behave as one run, even with the state stored between them, as the queue does.
func TestStagesShareTheirVerdictsAndSkips(t *testing.T) {
	var ran []string
	var saw bool
	registry := NewRegistry()
	add := func(name string, result *FilterResult, sawDKIM *bool) {
		registry.Register(name, func([]byte) (Filter, error) {
			return &stageFilter{name: name, result: result, ran: &ran, sawDKIM: sawDKIM}, nil
		})
	}
	add("dkim_verify", &FilterResult{Action: ActionContinue}, nil)
	add("contact_whitelist", &FilterResult{Action: ActionContinue, SkipFilters: []string{"rspamd"}}, nil)
	add("rspamd", &FilterResult{Action: ActionContinue}, nil)
	add("sieve", &FilterResult{Action: ActionContinue}, &saw)
	engine := NewEngine(registry, slog.Default())
	required, content := Split(&PipelineConfig{Filters: []FilterConfig{
		{Name: "dkim_verify", Enabled: true}, {Name: "contact_whitelist", Enabled: true},
		{Name: "rspamd", Enabled: true}, {Name: "sieve", Enabled: true},
	}})

	state := NewRunState()
	if _, err := engine.ExecuteStage(context.Background(), required, &EmailJSON{}, state); err != nil {
		t.Fatal(err)
	}
	stored, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	restored := NewRunState()
	if err := json.Unmarshal(stored, restored); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.ExecuteStage(context.Background(), content, &EmailJSON{}, restored); err != nil {
		t.Fatal(err)
	}
	if !equalNames(ran, []string{"dkim_verify", "contact_whitelist", "sieve"}) {
		t.Errorf("ran %v: rspamd should have been skipped", ran)
	}
	if !saw {
		t.Error("the content check could not see the required check's verdict")
	}
}
