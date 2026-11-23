package rules

import (
	"strings"
	"testing"
)

func validDefinition() Definition {
	return Definition{Name: "Invoices", Enabled: true, Conditions: []Condition{{Field: "subject", Operator: "contains", Value: "Invoice"}}, Actions: []Action{{Type: "fileinto", Value: "Finance"}}}
}
func TestNativeRuleValidationBoundsAndSupportedShapes(t *testing.T) {
	if err := Validate(validDefinition()); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		change func(*Definition)
	}{
		{"empty name", func(d *Definition) { d.Name = " " }},
		{"long name", func(d *Definition) { d.Name = strings.Repeat("a", 201) }},
		{"negative position", func(d *Definition) { d.Position = -1 }},
		{"large position", func(d *Definition) { d.Position = 1000001 }},
		{"no conditions", func(d *Definition) { d.Conditions = nil }},
		{"many conditions", func(d *Definition) { d.Conditions = make([]Condition, 21) }},
		{"no actions", func(d *Definition) { d.Actions = nil }},
		{"many actions", func(d *Definition) { d.Actions = make([]Action, 21) }},
		{"unknown field", func(d *Definition) { d.Conditions[0].Field = "regex" }},
		{"unknown operator", func(d *Definition) { d.Conditions[0].Operator = "regex" }},
		{"empty value", func(d *Definition) { d.Conditions[0].Value = "" }},
		{"long value", func(d *Definition) { d.Conditions[0].Value = strings.Repeat("a", 1025) }},
		{"line injection", func(d *Definition) { d.Conditions[0].Value = "x\nkeep;" }},
		{"unknown action", func(d *Definition) { d.Actions[0].Type = "javascript" }},
		{"bad folder", func(d *Definition) { d.Actions[0].Value = "../outside" }},
		{"bad flag", func(d *Definition) { d.Actions[0] = Action{Type: "flag", Value: `\Deleted`} }},
		{"bad forward", func(d *Definition) { d.Actions[0] = Action{Type: "forward", Value: "Person <person@example.test>"} }},
		{"line injection action", func(d *Definition) { d.Actions[0].Value = "x\nkeep;" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := validDefinition()
			tc.change(&d)
			if Validate(d) == nil {
				t.Fatal("invalid rule accepted")
			}
		})
	}
	for _, action := range []Action{{Type: "flag", Value: `\Seen`}, {Type: "flag", Value: `\Draft`}, {Type: "flag", Value: `\Flagged`}, {Type: "forward", Value: "copy@example.test"}} {
		d := validDefinition()
		d.Actions = []Action{action}
		if err := Validate(d); err != nil {
			t.Fatal(err)
		}
	}
	d := validDefinition()
	d.Name = strings.Repeat("界", 200)
	d.Conditions[0].Value = strings.Repeat("界", 1024)
	if err := Validate(d); err != nil {
		t.Fatal(err)
	}
}
func TestNativeRuleMatchUsesASCIICasemap(t *testing.T) {
	for _, tc := range []struct {
		op, wantValue, have string
		want                bool
	}{{"contains", "invoice", "Invoice 42", true}, {"is", "alice@example.test", "ALICE@example.test", true}, {"is", "invoice", "invoice 42", false}, {"contains", "Ä", "ä", false}, {"contains", "x", "nothing", false}} {
		if got := Match(Condition{Operator: tc.op, Value: tc.wantValue}, []string{tc.have}); got != tc.want {
			t.Errorf("%+v: %t", tc, got)
		}
	}
	if Match(Condition{Operator: "is", Value: "x"}, nil) {
		t.Fatal("missing field matched")
	}
}
func TestNativeRuleFlagRestoreMapping(t *testing.T) {
	for flag, column := range map[string]string{`\Seen`: "is_read", `\Flagged`: "is_flagged", `\Draft`: "is_draft", "unknown": ""} {
		if FlagColumn(flag) != column {
			t.Fatal(flag)
		}
	}
	if Bool("false") || !Bool("true") {
		t.Fatal("flag before value changed")
	}
}
