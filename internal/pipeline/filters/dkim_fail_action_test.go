package filters

import (
	"context"
	"testing"

	"github.com/rest-mail/go-dkim"
	"github.com/restmail/restmail/internal/pipeline"
)

// stubDKIM makes dkim_verify see results as the verdicts on the message's
// signatures.
func stubDKIM(t *testing.T, results ...dkim.VerifyResult) {
	t.Helper()
	orig := dkimVerify
	dkimVerify = func(context.Context, []byte, dkim.TXTResolver) []dkim.VerifyResult { return results }
	t.Cleanup(func() { dkimVerify = orig })
}

// signedEmail is a message whose raw source dkim_verify can verify.
func signedEmail() *pipeline.EmailJSON {
	return &pipeline.EmailJSON{
		Metadata: map[string]string{"raw_message": "DKIM-Signature: v=1; d=sender.example; s=s1\r\nFrom: a@sender.example\r\n\r\nbody\r\n"},
	}
}

// TestDKIMVerify_FailAction proves fail_action ("Action on DKIM Fail") acts on
// the typed overall verdict, and only when it is fail: a signature that fails
// and none that passes. none, neutral, temperror and permerror never act, a
// passing signature outweighs a failing one, and tag (the default) leaves the
// decision to DMARC (#396).
func TestDKIMVerify_FailAction(t *testing.T) {
	sig := func(result, domain string) dkim.VerifyResult {
		return dkim.VerifyResult{Result: result, Domain: domain, Selector: "s1"}
	}
	cases := []struct {
		name    string
		config  string
		results []dkim.VerifyResult
		want    pipeline.Action
	}{
		{"reject on fail", `{"fail_action":"reject"}`, []dkim.VerifyResult{sig(dkim.ResultFail, "sender.example")}, pipeline.ActionReject},
		{"quarantine on fail", `{"fail_action":"quarantine"}`, []dkim.VerifyResult{sig(dkim.ResultFail, "sender.example")}, pipeline.ActionQuarantine},
		{"tag on fail", `{"fail_action":"tag"}`, []dkim.VerifyResult{sig(dkim.ResultFail, "sender.example")}, pipeline.ActionContinue},
		{"default on fail", ``, []dkim.VerifyResult{sig(dkim.ResultFail, "sender.example")}, pipeline.ActionContinue},
		{"reject, no signature", `{"fail_action":"reject"}`, nil, pipeline.ActionContinue},
		{"reject on none", `{"fail_action":"reject"}`, []dkim.VerifyResult{sig(dkim.ResultNone, "sender.example")}, pipeline.ActionContinue},
		{"reject on neutral", `{"fail_action":"reject"}`, []dkim.VerifyResult{sig(dkim.ResultNeutral, "sender.example")}, pipeline.ActionContinue},
		{"reject on temperror", `{"fail_action":"reject"}`, []dkim.VerifyResult{sig(dkim.ResultTempError, "sender.example")}, pipeline.ActionContinue},
		{"reject on permerror", `{"fail_action":"reject"}`, []dkim.VerifyResult{sig(dkim.ResultPermError, "sender.example")}, pipeline.ActionContinue},
		{"reject, pass outweighs fail", `{"fail_action":"reject"}`, []dkim.VerifyResult{sig(dkim.ResultFail, "forwarder.example"), sig(dkim.ResultPass, "sender.example")}, pipeline.ActionContinue},
		{"reject, fail outweighs temperror", `{"fail_action":"reject"}`, []dkim.VerifyResult{sig(dkim.ResultTempError, "sender.example"), sig(dkim.ResultFail, "other.example")}, pipeline.ActionReject},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stubDKIM(t, tc.results...)
			f, err := NewDKIMVerify([]byte(tc.config))
			if err != nil {
				t.Fatalf("NewDKIMVerify(%q): %v", tc.config, err)
			}
			auth := &pipeline.AuthResults{}
			res, err := f.Execute(pipeline.WithAuthResults(context.Background(), auth), signedEmail())
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if res.Action != tc.want {
				t.Fatalf("action = %q, want %q (log %+v)", res.Action, tc.want, res.Log)
			}
			if tc.want == pipeline.ActionReject && res.RejectMsg == "" {
				t.Error("a reject carries no RejectMsg")
			}
			// The verdicts are recorded for DMARC whatever fail_action does.
			if len(tc.results) > 0 && len(auth.List()) != len(tc.results) {
				t.Errorf("recorded %d verdicts, want %d", len(auth.List()), len(tc.results))
			}
			if got := pipeline.Reason("dkim_verify", res.Action, res.Log.Result); res.Action != pipeline.ActionContinue && got != pipeline.ReasonDKIMFail {
				t.Errorf("reason = %q, want %q", got, pipeline.ReasonDKIMFail)
			}
		})
	}
}

// TestDKIMVerify_FailActionNeedsRawSource proves a signature that could not be
// verified for want of the raw source is neutral, and never acts.
func TestDKIMVerify_FailActionNeedsRawSource(t *testing.T) {
	f, err := NewDKIMVerify([]byte(`{"fail_action":"reject"}`))
	if err != nil {
		t.Fatal(err)
	}
	email := &pipeline.EmailJSON{}
	email.Headers.Raw = map[string][]string{"Dkim-Signature": {"v=1; d=sender.example; s=s1"}}
	res, err := f.Execute(context.Background(), email)
	if err != nil {
		t.Fatal(err)
	}
	if res.Action != pipeline.ActionContinue || res.Log.Result != dkim.ResultNeutral {
		t.Fatalf("action %q result %q, want continue and neutral", res.Action, res.Log.Result)
	}
}

// TestDKIMVerify_BadFailAction proves an unknown or malformed fail_action is a
// configuration error rather than a silent tag.
func TestDKIMVerify_BadFailAction(t *testing.T) {
	for _, bad := range []string{`{"fail_action":"bounce"}`, `{"fail_action":`} {
		if _, err := NewDKIMVerify([]byte(bad)); err == nil {
			t.Errorf("NewDKIMVerify(%s) succeeded, want an error", bad)
		}
	}
}
