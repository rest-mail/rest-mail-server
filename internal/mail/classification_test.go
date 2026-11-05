package mail

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestClassifyHeaders(t *testing.T) {
	for _, test := range []struct {
		name    string
		headers map[string][]string
		want    string
	}{
		{"empty", nil, "person"},
		{"person", map[string][]string{"From": {"Alice <alice@example.test>"}}, "person"},
		{"unrelated precedence", map[string][]string{"Precedence": {"first-class"}}, "person"},
		{"malformed precedence", map[string][]string{"Precedence": {"bulk, personal"}}, "person"},
		{"list id", map[string][]string{"List-Id": {"People talking <discussion.example.test>"}}, "newsletter"},
		{"list unsubscribe", map[string][]string{"List-Unsubscribe": {"<mailto:leave@example.test>, <https://example.test/leave>"}}, "newsletter"},
		{"case and folding", map[string][]string{"lIsT-iD": {"A list\r\n <news.example.test>"}}, "newsletter"},
		{"line feed folding", map[string][]string{"List-Id": {"A list\n\t<news.example.test>"}}, "newsletter"},
		{"folding across lines", map[string][]string{"LIST-UNSUBSCRIBE": {"<mailto:leave@example.test>,\r\n   \r\n <https://example.test/leave>"}}, "newsletter"},
		{"folded precedence", map[string][]string{"precedence": {"\r\n bulk"}}, "notification"},
		{"empty list id", map[string][]string{"List-Id": {""}}, "person"},
		{"malformed list id", map[string][]string{"List-Id": {"not a list"}}, "person"},
		{"malformed id dots", map[string][]string{"List-Id": {"<news..example.test>"}}, "person"},
		{"overlong list id", map[string][]string{"List-Id": {"<" + strings.Repeat("a", 252) + ".com>"}}, "person"},
		{"maximum list id", map[string][]string{"List-Id": {"<" + strings.Repeat("a", 251) + ".com>"}}, "newsletter"},
		{"bare line feed injection", map[string][]string{"List-Id": {"<news.example.test>\nX-Test: yes"}}, "person"},
		{"injected header", map[string][]string{"List-Id": {"<news.example.test>\r\nX-Test: yes"}}, "person"},
		{"duplicate id", map[string][]string{"List-Id": {"<a.example.test>", "<b.example.test>"}}, "person"},
		{"duplicate casing", map[string][]string{"List-Id": {"<a.example.test>"}, "list-id": {"<b.example.test>"}}, "person"},
		{"malformed unsubscribe", map[string][]string{"List-Unsubscribe": {"click here"}}, "person"},
		{"empty unsubscribe url", map[string][]string{"List-Unsubscribe": {"<https://>"}}, "person"},
		{"invalid mailto", map[string][]string{"List-Unsubscribe": {"<mailto:@>"}}, "person"},
		{"unsafe unsubscribe", map[string][]string{"List-Unsubscribe": {"<javascript:alert(1)>"}}, "person"},
		{"partial unsubscribe", map[string][]string{"List-Unsubscribe": {"<https://example.test/leave>, garbage"}}, "person"},
		{"bulk", map[string][]string{"Precedence": {"BULK"}}, "notification"},
		{"list precedence alone", map[string][]string{"Precedence": {"list"}}, "notification"},
		{"junk", map[string][]string{"Precedence": {"junk"}}, "notification"},
		{"auto generated", map[string][]string{"Auto-Submitted": {"auto-generated"}}, "notification"},
		{"auto reply parameters", map[string][]string{"Auto-Submitted": {`Auto-Replied; owner-email="alice@example.test"`}}, "notification"},
		{"not automated", map[string][]string{"Auto-Submitted": {"no"}}, "person"},
		{"unknown automation", map[string][]string{"Auto-Submitted": {"maybe"}}, "person"},
		{"malformed automation", map[string][]string{"Auto-Submitted": {"auto-generated; broken"}}, "person"},
		{"no sender guessing", map[string][]string{"From": {"noreply@example.test"}, "Subject": {"Your receipt"}}, "person"},
		{"receipt indistinguishable", map[string][]string{"Auto-Submitted": {"auto-generated"}, "Subject": {"Your receipt"}}, "notification"},
		{"list wins automation", map[string][]string{"List-Id": {"<news.example.test>"}, "Auto-Submitted": {"auto-generated"}}, "newsletter"},
		{"list wins precedence", map[string][]string{"List-Unsubscribe": {"<https://example.test/leave>"}, "Precedence": {"bulk"}}, "newsletter"},
		{"automation before precedence", map[string][]string{"Auto-Submitted": {"auto-generated"}, "Precedence": {"first-class"}}, "notification"},
		{"forged list header on a person's mail", map[string][]string{"From": {"Alice <alice@example.test>"}, "List-Id": {"<forged.example.test>"}}, "newsletter"},
	} {
		t.Run(test.name, func(t *testing.T) {
			before, _ := json.Marshal(test.headers)
			for iteration := 0; iteration < 50; iteration++ {
				if got := ClassifyHeaders(test.headers); got != test.want {
					t.Fatalf("class = %q, want %q", got, test.want)
				}
			}
			after, _ := json.Marshal(test.headers)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("classification mutated headers")
			}
		})
	}
}

func TestClassifyWithRuleResults(t *testing.T) {
	list := map[string][]string{"List-Id": {"<news.example.test>"}}
	automated := map[string][]string{"Precedence": {"bulk"}}
	for _, test := range []struct {
		name      string
		headers   map[string][]string
		ruleClass string
		want      string
	}{
		{"no rule, no signals", nil, "", ClassPerson},
		{"no rule keeps the list class", list, "", ClassNewsletter},
		{"no rule keeps the automated class", automated, "", ClassNotification},
		{"rule on a person's mail", nil, ClassReceipt, ClassReceipt},
		{"rule beats list headers", list, ClassReceipt, ClassReceipt},
		{"rule beats automation headers", automated, ClassReceipt, ClassReceipt},
		{"a rule cannot make a person's mail collapse", nil, ClassNewsletter, ClassPerson},
		{"a rule cannot name another class", nil, ClassNotification, ClassPerson},
		{"an unknown rule class is ignored", list, "bogus", ClassNewsletter},
		{"the rule class is exact", nil, "RECEIPT", ClassPerson},
	} {
		t.Run(test.name, func(t *testing.T) {
			for iteration := 0; iteration < 50; iteration++ {
				if got := Classify(test.headers, test.ruleClass); got != test.want {
					t.Fatalf("class = %q, want %q", got, test.want)
				}
			}
		})
	}
}

func TestRuleClassOf(t *testing.T) {
	for _, test := range []struct {
		keywords []string
		want     string
	}{
		{nil, ""},
		{[]string{`\\Seen`, "$Label"}, ""},
		{[]string{"receipt"}, ""},
		{[]string{"$receipts"}, ""},
		{[]string{"$newsletter", "$notification", "$person"}, ""},
		{[]string{SieveReceiptKeyword}, ClassReceipt},
		{[]string{`\\Flagged`, "$RECEIPT"}, ClassReceipt},
	} {
		if got := RuleClassOf(test.keywords); got != test.want {
			t.Errorf("RuleClassOf(%q) = %q, want %q", test.keywords, got, test.want)
		}
	}
}
