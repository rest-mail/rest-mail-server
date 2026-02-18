package pipeline

import (
	"context"
	"encoding/json"
	"testing"
)

// ── Mock Filters ────────────────────────────────────────────────────

// mockActionFilter is a simple action filter for testing.
type mockActionFilter struct {
	name        string
	action      Action
	rejectMsg   string
	skipFilters []string
}

func (f *mockActionFilter) Name() string     { return f.name }
func (f *mockActionFilter) Type() FilterType { return FilterTypeAction }

func (f *mockActionFilter) Execute(_ context.Context, _ *EmailJSON) (*FilterResult, error) {
	return &FilterResult{
		Type:        FilterTypeAction,
		Action:      f.action,
		RejectMsg:   f.rejectMsg,
		SkipFilters: f.skipFilters,
		Log: FilterLog{
			Filter: f.name,
			Result: string(f.action),
		},
	}, nil
}

// mockTransformFilter modifies the email subject.
type mockTransformFilter struct {
	name       string
	newSubject string
}

func (f *mockTransformFilter) Name() string     { return f.name }
func (f *mockTransformFilter) Type() FilterType { return FilterTypeTransform }

func (f *mockTransformFilter) Execute(_ context.Context, email *EmailJSON) (*FilterResult, error) {
	modified := *email
	modified.Headers.Subject = f.newSubject
	return &FilterResult{
		Type:    FilterTypeTransform,
		Action:  ActionContinue,
		Message: &modified,
		Log: FilterLog{
			Filter: f.name,
			Result: "transformed",
			Detail: "subject changed to: " + f.newSubject,
		},
	}, nil
}

// ── Registry Tests ──────────────────────────────────────────────────

func TestRegistry_RegisterAndCreate(t *testing.T) {
	reg := NewRegistry()
	reg.Register("mock_action", func(config []byte) (Filter, error) {
		return &mockActionFilter{name: "mock_action", action: ActionContinue}, nil
	})

	f, err := reg.Create("mock_action", nil)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if f.Name() != "mock_action" {
		t.Errorf("expected name 'mock_action', got %q", f.Name())
	}
	if f.Type() != FilterTypeAction {
		t.Errorf("expected type %q, got %q", FilterTypeAction, f.Type())
	}

	result, err := f.Execute(context.Background(), &EmailJSON{})
	if err != nil {
		t.Fatalf("expected no error on Execute, got %v", err)
	}
	if result.Action != ActionContinue {
		t.Errorf("expected action %q, got %q", ActionContinue, result.Action)
	}
}

func TestRegistry_UnknownFilter(t *testing.T) {
	reg := NewRegistry()

	_, err := reg.Create("does_not_exist", nil)
	if err == nil {
		t.Fatal("expected error for unknown filter, got nil")
	}
	if got := err.Error(); got != "unknown filter: does_not_exist" {
		t.Errorf("unexpected error message: %q", got)
	}
}

func TestRegistry_List(t *testing.T) {
	reg := NewRegistry()
	reg.Register("alpha", func([]byte) (Filter, error) { return nil, nil })
	reg.Register("bravo", func([]byte) (Filter, error) { return nil, nil })
	reg.Register("charlie", func([]byte) (Filter, error) { return nil, nil })

	names := reg.List()
	if len(names) != 3 {
		t.Fatalf("expected 3 names, got %d", len(names))
	}

	nameSet := make(map[string]bool)
	for _, n := range names {
		nameSet[n] = true
	}
	for _, want := range []string{"alpha", "bravo", "charlie"} {
		if !nameSet[want] {
			t.Errorf("expected %q in list, not found", want)
		}
	}
}

// ── Engine Tests ────────────────────────────────────────────────────

func newTestEngine(reg *Registry) *Engine {
	return NewEngine(reg, nil)
}

func TestEngine_EmptyPipeline(t *testing.T) {
	reg := NewRegistry()
	eng := newTestEngine(reg)

	cfg := &PipelineConfig{
		Direction: "inbound",
		Filters:   []FilterConfig{},
		Active:    true,
	}

	email := &EmailJSON{
		Headers: Headers{Subject: "test"},
	}

	result, err := eng.Execute(context.Background(), cfg, email)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.FinalAction != ActionContinue {
		t.Errorf("expected ActionContinue, got %q", result.FinalAction)
	}
	if len(result.Steps) != 0 {
		t.Errorf("expected 0 steps, got %d", len(result.Steps))
	}
}

func TestEngine_ActionContinue(t *testing.T) {
	reg := NewRegistry()
	reg.Register("pass_filter", func([]byte) (Filter, error) {
		return &mockActionFilter{name: "pass_filter", action: ActionContinue}, nil
	})
	eng := newTestEngine(reg)

	cfg := &PipelineConfig{
		Direction: "inbound",
		Filters: []FilterConfig{
			{Name: "pass_filter", Type: FilterTypeAction, Enabled: true},
		},
		Active: true,
	}

	email := &EmailJSON{Headers: Headers{Subject: "hello"}}

	result, err := eng.Execute(context.Background(), cfg, email)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.FinalAction != ActionContinue {
		t.Errorf("expected ActionContinue, got %q", result.FinalAction)
	}
	if len(result.Steps) != 1 {
		t.Fatalf("expected 1 step, got %d", len(result.Steps))
	}
	if result.Steps[0].Action != ActionContinue {
		t.Errorf("step action: expected %q, got %q", ActionContinue, result.Steps[0].Action)
	}
}

func TestEngine_ActionReject(t *testing.T) {
	reg := NewRegistry()
	reg.Register("reject_filter", func([]byte) (Filter, error) {
		return &mockActionFilter{
			name:      "reject_filter",
			action:    ActionReject,
			rejectMsg: "550 Rejected by policy",
		}, nil
	})
	reg.Register("after_reject", func([]byte) (Filter, error) {
		return &mockActionFilter{name: "after_reject", action: ActionContinue}, nil
	})
	eng := newTestEngine(reg)

	cfg := &PipelineConfig{
		Direction: "inbound",
		Filters: []FilterConfig{
			{Name: "reject_filter", Type: FilterTypeAction, Enabled: true},
			{Name: "after_reject", Type: FilterTypeAction, Enabled: true},
		},
		Active: true,
	}

	email := &EmailJSON{Headers: Headers{Subject: "spam"}}

	result, err := eng.Execute(context.Background(), cfg, email)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.FinalAction != ActionReject {
		t.Errorf("expected ActionReject, got %q", result.FinalAction)
	}
	if result.RejectMsg != "550 Rejected by policy" {
		t.Errorf("unexpected reject message: %q", result.RejectMsg)
	}
	// The second filter should NOT have run because reject stops the pipeline.
	if len(result.Steps) != 1 {
		t.Errorf("expected 1 step (pipeline should stop on reject), got %d", len(result.Steps))
	}
}

func TestEngine_TransformModifiesEmail(t *testing.T) {
	reg := NewRegistry()
	reg.Register("tag_subject", func([]byte) (Filter, error) {
		return &mockTransformFilter{name: "tag_subject", newSubject: "[TAGGED] hello"}, nil
	})
	eng := newTestEngine(reg)

	cfg := &PipelineConfig{
		Direction: "inbound",
		Filters: []FilterConfig{
			{Name: "tag_subject", Type: FilterTypeTransform, Enabled: true},
		},
		Active: true,
	}

	email := &EmailJSON{Headers: Headers{Subject: "hello"}}

	result, err := eng.Execute(context.Background(), cfg, email)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.FinalAction != ActionContinue {
		t.Errorf("expected ActionContinue, got %q", result.FinalAction)
	}
	if result.FinalEmail.Headers.Subject != "[TAGGED] hello" {
		t.Errorf("expected subject '[TAGGED] hello', got %q", result.FinalEmail.Headers.Subject)
	}
}

func TestEngine_DisabledFilter(t *testing.T) {
	reg := NewRegistry()
	reg.Register("should_not_run", func([]byte) (Filter, error) {
		return &mockActionFilter{name: "should_not_run", action: ActionReject, rejectMsg: "bad"}, nil
	})
	eng := newTestEngine(reg)

	cfg := &PipelineConfig{
		Direction: "inbound",
		Filters: []FilterConfig{
			{Name: "should_not_run", Type: FilterTypeAction, Enabled: false},
		},
		Active: true,
	}

	email := &EmailJSON{Headers: Headers{Subject: "test"}}

	result, err := eng.Execute(context.Background(), cfg, email)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.FinalAction != ActionContinue {
		t.Errorf("expected ActionContinue (disabled filter skipped), got %q", result.FinalAction)
	}
	if len(result.Steps) != 0 {
		t.Errorf("expected 0 steps (disabled filters are not executed), got %d", len(result.Steps))
	}
}

func TestEngine_SkipFilters(t *testing.T) {
	reg := NewRegistry()
	reg.Register("skipper", func([]byte) (Filter, error) {
		return &mockActionFilter{
			name:        "skipper",
			action:      ActionContinue,
			skipFilters: []string{"victim"},
		}, nil
	})
	reg.Register("victim", func([]byte) (Filter, error) {
		return &mockActionFilter{name: "victim", action: ActionReject, rejectMsg: "should not reach"}, nil
	})
	reg.Register("survivor", func([]byte) (Filter, error) {
		return &mockActionFilter{name: "survivor", action: ActionContinue}, nil
	})
	eng := newTestEngine(reg)

	cfg := &PipelineConfig{
		Direction: "inbound",
		Filters: []FilterConfig{
			{Name: "skipper", Type: FilterTypeAction, Enabled: true},
			{Name: "victim", Type: FilterTypeAction, Enabled: true},
			{Name: "survivor", Type: FilterTypeAction, Enabled: true},
		},
		Active: true,
	}

	email := &EmailJSON{Headers: Headers{Subject: "test"}}

	result, err := eng.Execute(context.Background(), cfg, email)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.FinalAction != ActionContinue {
		t.Errorf("expected ActionContinue, got %q", result.FinalAction)
	}
	if len(result.Steps) != 3 {
		t.Fatalf("expected 3 steps, got %d", len(result.Steps))
	}

	// Step 0: skipper ran normally.
	if result.Steps[0].Skipped {
		t.Errorf("step 0 (skipper) should not be skipped")
	}
	// Step 1: victim was skipped.
	if !result.Steps[1].Skipped {
		t.Errorf("step 1 (victim) should be skipped")
	}
	if result.Steps[1].SkipReason == "" {
		t.Errorf("step 1 (victim) should have a skip reason")
	}
	// Step 2: survivor ran normally.
	if result.Steps[2].Skipped {
		t.Errorf("step 2 (survivor) should not be skipped")
	}
}

func TestEngine_UnskippableFilter(t *testing.T) {
	reg := NewRegistry()
	reg.Register("skipper", func([]byte) (Filter, error) {
		return &mockActionFilter{
			name:        "skipper",
			action:      ActionContinue,
			skipFilters: []string{"unskippable_filter"},
		}, nil
	})
	reg.Register("unskippable_filter", func([]byte) (Filter, error) {
		return &mockActionFilter{name: "unskippable_filter", action: ActionContinue}, nil
	})
	eng := newTestEngine(reg)

	cfg := &PipelineConfig{
		Direction: "inbound",
		Filters: []FilterConfig{
			{Name: "skipper", Type: FilterTypeAction, Enabled: true},
			{Name: "unskippable_filter", Type: FilterTypeAction, Enabled: true, Unskippable: true},
		},
		Active: true,
	}

	email := &EmailJSON{Headers: Headers{Subject: "test"}}

	result, err := eng.Execute(context.Background(), cfg, email)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Steps) != 2 {
		t.Fatalf("expected 2 steps, got %d", len(result.Steps))
	}

	// The unskippable filter must NOT be skipped even though it is in the skip set.
	if result.Steps[1].Skipped {
		t.Errorf("unskippable_filter should NOT be skipped")
	}
	if result.Steps[1].Action != ActionContinue {
		t.Errorf("unskippable_filter should have ActionContinue, got %q", result.Steps[1].Action)
	}
}

// ── Template Tests ──────────────────────────────────────────────────

func TestDefaultInboundPipeline(t *testing.T) {
	p := DefaultInboundPipeline(42)

	if p.DomainID != 42 {
		t.Errorf("expected DomainID 42, got %d", p.DomainID)
	}
	if p.Direction != "inbound" {
		t.Errorf("expected direction 'inbound', got %q", p.Direction)
	}
	if !p.Active {
		t.Error("expected pipeline to be active")
	}

	expectedFilters := []string{
		"size_check", "spf_check", "dkim_verify", "arc_verify", "dmarc_check",
		"domain_allowlist", "contact_whitelist", "greylist",
		"header_validate", "recipient_check", "extract_attachments", "sieve",
	}
	if len(p.Filters) != len(expectedFilters) {
		t.Fatalf("expected %d filters, got %d", len(expectedFilters), len(p.Filters))
	}
	for i, want := range expectedFilters {
		if p.Filters[i].Name != want {
			t.Errorf("filter[%d]: expected %q, got %q", i, want, p.Filters[i].Name)
		}
	}
}

func TestDefaultOutboundPipeline(t *testing.T) {
	p := DefaultOutboundPipeline(99)

	if p.DomainID != 99 {
		t.Errorf("expected DomainID 99, got %d", p.DomainID)
	}
	if p.Direction != "outbound" {
		t.Errorf("expected direction 'outbound', got %q", p.Direction)
	}
	if !p.Active {
		t.Error("expected pipeline to be active")
	}

	expectedFilters := []string{
		"sender_verify", "rate_limit", "header_cleanup", "arc_seal", "dkim_sign",
	}
	if len(p.Filters) != len(expectedFilters) {
		t.Fatalf("expected %d filters, got %d", len(expectedFilters), len(p.Filters))
	}
	for i, want := range expectedFilters {
		if p.Filters[i].Name != want {
			t.Errorf("filter[%d]: expected %q, got %q", i, want, p.Filters[i].Name)
		}
	}
}

func TestDefaultPipelineJSON(t *testing.T) {
	t.Run("inbound", func(t *testing.T) {
		raw := DefaultPipelineJSON("inbound")
		var filters []FilterConfig
		if err := json.Unmarshal(raw, &filters); err != nil {
			t.Fatalf("failed to unmarshal inbound JSON: %v", err)
		}
		if len(filters) == 0 {
			t.Fatal("expected non-empty filter list for inbound")
		}
		if filters[0].Name != "size_check" {
			t.Errorf("expected first inbound filter to be 'size_check', got %q", filters[0].Name)
		}
	})

	t.Run("outbound", func(t *testing.T) {
		raw := DefaultPipelineJSON("outbound")
		var filters []FilterConfig
		if err := json.Unmarshal(raw, &filters); err != nil {
			t.Fatalf("failed to unmarshal outbound JSON: %v", err)
		}
		if len(filters) == 0 {
			t.Fatal("expected non-empty filter list for outbound")
		}
		if filters[0].Name != "sender_verify" {
			t.Errorf("expected first outbound filter to be 'sender_verify', got %q", filters[0].Name)
		}
	})

	t.Run("unknown direction", func(t *testing.T) {
		raw := DefaultPipelineJSON("invalid")
		if string(raw) != "[]" {
			t.Errorf("expected '[]' for unknown direction, got %q", string(raw))
		}
	})
}
