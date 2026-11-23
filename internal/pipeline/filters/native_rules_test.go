package filters

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/pipeline"
	"github.com/rest-mail/rest-mail-server/internal/rules"
)

func nativeRuleFixture() rules.Rule {
	return rules.Rule{ID: 42, Version: 3, Definition: rules.Definition{Name: "Invoices", Enabled: true, Conditions: []rules.Condition{{Field: "subject", Operator: "contains", Value: `Invoice "42"`}, {Field: "from", Operator: "is", Value: "alice@remote.test"}, {Field: "to", Operator: "contains", Value: "owner@"}}, Actions: []rules.Action{{Type: "fileinto", Value: `Bills "42"`}, {Type: "flag", Value: `\Flagged`}, {Type: "forward", Value: "copy@local.test"}}}}
}
func nativeRuleEmail(subject string) *pipeline.EmailJSON {
	return &pipeline.EmailJSON{Envelope: pipeline.Envelope{RcptTo: []string{"owner@local.test"}}, Headers: pipeline.Headers{Subject: subject, From: pipeline.Addresses(pipeline.Address{Address: "ALICE@remote.test"}), To: pipeline.Addresses(pipeline.Address{Address: "owner@local.test"})}}
}
func TestNativeRuleExportMatchesIndependentSieveExecution(t *testing.T) {
	rule := nativeRuleFixture()
	disabled := rule
	disabled.ID = 43
	disabled.Enabled = false
	disabled.Actions = []rules.Action{{Type: "fileinto", Value: "Wrong"}}
	rs := []rules.Rule{rule, disabled}
	exported := rules.Export(rs)
	if err := ValidateSieveForInstall(exported); err != nil {
		t.Fatalf("export not accepted by real Sieve gate: %s: %v", exported, err)
	}
	if strings.Count(exported, "# restmail-rule 42 ") != 1 || strings.Count(exported, "# restmail-rule 43 ") != 1 || strings.Contains(exported, `fileinto "Wrong"`) {
		t.Fatal(exported)
	}
	encoded, _ := json.Marshal(rs)
	native, err := NewNativeRulesWithPolicy(SieveRedirectPolicy{})(encoded)
	if err != nil {
		t.Fatal(err)
	}
	sieveBytes, _ := json.Marshal(map[string]string{"script": exported})
	legacy, err := NewSieve(sieveBytes)
	if err != nil {
		t.Fatal(err)
	}
	for _, subject := range []string{`Invoice "42" due`, "ordinary"} {
		want, err := legacy.Execute(t.Context(), nativeRuleEmail(subject))
		if err != nil {
			t.Fatal(err)
		}
		email := nativeRuleEmail(subject)
		got, err := native.Execute(t.Context(), email)
		if err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"deliver_to_folder", "imap_flags", "redirect_to", "redirect_suppress_keep"} {
			if got.Message.Metadata[key] != want.Message.Metadata[key] {
				t.Errorf("%s %s native=%q Sieve=%q", subject, key, got.Message.Metadata[key], want.Message.Metadata[key])
			}
		}
		if subject == "ordinary" && len(got.Message.RuleEffects) != 0 {
			t.Fatal("non-match attributed")
		}
		if subject != "ordinary" && (len(got.Message.RuleEffects) != 3 || len(got.Message.RuleMatches) != 1) {
			t.Fatalf("wrong effects: %+v", got.Message.RuleEffects)
		}
		if len(email.Metadata) != 0 || len(email.RuleEffects) != 0 {
			t.Fatal("input mutated")
		}
	}
}
func TestNativeRuleForwardPolicyLoopsAndCancellation(t *testing.T) {
	rule := nativeRuleFixture()
	rule.Conditions = rule.Conditions[:1]
	for _, target := range []string{"outside@external.test", "owner@local.test", "delivered@local.test"} {
		rule.Actions = []rules.Action{{Type: "forward", Value: target}}
		raw, _ := json.Marshal([]rules.Rule{rule})
		filter, err := NewNativeRulesWithPolicy(SieveRedirectPolicy{})(raw)
		if err != nil {
			t.Fatal(err)
		}
		email := nativeRuleEmail(`Invoice "42"`)
		email.Headers.Raw = map[string][]string{"Delivered-To": {"delivered@local.test"}}
		result, err := filter.Execute(t.Context(), email)
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Message.RuleEffects) != 0 || result.Message.Metadata["redirect_to"] != "" {
			t.Fatalf("policy bypass for %s", target)
		}
	}
	raw, _ := json.Marshal([]rules.Rule{nativeRuleFixture()})
	filter, err := NewNativeRulesWithPolicy(SieveRedirectPolicy{})(raw)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := filter.Execute(ctx, nativeRuleEmail("invoice")); err == nil {
		t.Fatal("cancellation ignored")
	}
	for _, raw := range [][]byte{[]byte(`{}`), []byte(`[{}]`), []byte(`[ {"id":1,"version":1} ]`)} {
		if _, err := NewNativeRulesWithPolicy(SieveRedirectPolicy{})(raw); err == nil {
			t.Fatalf("bad rule accepted %s", raw)
		}
	}
	tooMany, _ := json.Marshal(make([]rules.Rule, rules.MaxRules+1))
	if _, err := NewNativeRulesWithPolicy(SieveRedirectPolicy{})(tooMany); err == nil {
		t.Fatal("unbounded rule set accepted")
	}
}
