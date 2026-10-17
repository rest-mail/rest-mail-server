package pipeline

import (
	"context"
	"testing"
)

// TestAuthResultsWriter checks how verdicts are written as
// Authentication-Results: our authserv-id first, one resinfo per verdict, and a
// property only when its domain is a plain host name, so no recorded value can
// add a result of its own.
func TestAuthResultsWriter(t *testing.T) {
	rs := []AuthResult{
		{Method: AuthSPF, Result: "pass", Property: PropHelo, Domain: "mx.example.com"},
		{Method: AuthSPF, Result: "pass", Property: PropMailFrom, Domain: "victim.example;dkim=pass"},
		{Method: AuthDKIM, Result: "pass", Property: PropHeaderD, Domain: "example.com"},
		{Method: AuthARC, Result: "none", Domain: "sealer.example"},
	}
	wantValue := "restmail; spf=pass smtp.helo=mx.example.com; spf=pass; dkim=pass header.d=example.com; arc=none"
	if got := AuthResultsValue(rs); got != wantValue {
		t.Errorf("AuthResultsValue = %q, want %q", got, wantValue)
	}
	wantHeader := "Authentication-Results: restmail;\r\n\tspf=pass smtp.helo=mx.example.com;\r\n\tspf=pass;\r\n\tdkim=pass header.d=example.com;\r\n\tarc=none\r\n"
	if got := AuthResultsHeader(rs); got != wantHeader {
		t.Errorf("AuthResultsHeader = %q, want %q", got, wantHeader)
	}
	if got := AuthResultsValue(nil); got != "restmail; none" {
		t.Errorf("AuthResultsValue(nil) = %q", got)
	}
	if got := AuthResultsHeader(nil); got != "" {
		t.Errorf("AuthResultsHeader(nil) = %q, want empty", got)
	}
}

// TestAuthResults_NilAndFind checks that outside a pipeline run the verdicts
// are a nil *AuthResults that records and holds nothing, and that Find selects
// by method and property.
func TestAuthResults_NilAndFind(t *testing.T) {
	var none *AuthResults
	none.Add(AuthResult{Method: AuthSPF, Result: "pass"})
	if got := none.List(); got != nil {
		t.Errorf("nil AuthResults holds %+v", got)
	}
	if a := AuthResultsFromContext(context.Background()); a != nil {
		t.Errorf("a context outside a run carries %+v", a)
	}

	a := &AuthResults{}
	a.Add(AuthResult{Method: AuthSPF, Result: "fail", Property: PropHelo, Domain: "mx.example.com"},
		AuthResult{Method: AuthSPF, Result: "pass", Property: PropMailFrom, Domain: "example.com"})
	got := AuthResultsFromContext(WithAuthResults(context.Background(), a)).Find(AuthSPF, PropMailFrom)
	if len(got) != 1 || got[0].Result != "pass" {
		t.Errorf("Find(spf, smtp.mailfrom) = %+v", got)
	}
}

// recordingFilter records one verdict, as an authentication filter does.
type recordingFilter struct{}

func (recordingFilter) Name() string     { return "recording" }
func (recordingFilter) Type() FilterType { return FilterTypeAction }
func (recordingFilter) Execute(ctx context.Context, _ *EmailJSON) (*FilterResult, error) {
	AuthResultsFromContext(ctx).Add(AuthResult{Method: AuthSPF, Result: "pass", Property: PropMailFrom, Domain: "example.com"})
	return &FilterResult{Type: FilterTypeAction, Action: ActionContinue}, nil
}

// TestEngine_AuthResultsPerRun checks that every run starts with no verdicts
// and returns the ones its filters recorded.
func TestEngine_AuthResultsPerRun(t *testing.T) {
	reg := NewRegistry()
	reg.Register("recording", func([]byte) (Filter, error) { return recordingFilter{}, nil })
	cfg := &PipelineConfig{Filters: []FilterConfig{{Name: "recording", Type: FilterTypeAction, Enabled: true}}}
	e := NewEngine(reg, nil)
	for i := 0; i < 2; i++ {
		res, err := e.Execute(context.Background(), cfg, &EmailJSON{})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Auth) != 1 {
			t.Fatalf("run %d: Auth = %+v, want the one verdict of this run", i, res.Auth)
		}
	}
}
