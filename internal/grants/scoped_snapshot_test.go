package grants

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestScopedSnapshot(t *testing.T) {
	h := HeldOf(Entry{All, "queue:read"}, Entry{"domain:a.test", "domains:read"}, Entry{"domain:a.test", "domains:read"}, Entry{"mailbox:a@a.test", "mailboxes:write"}, Entry{"domain:a.test", Wildcard})
	want := []Entry{{"domain:a.test", Wildcard}, {"domain:a.test", "domains:read"}, {"mailbox:a@a.test", "mailboxes:write"}}
	if got := h.ScopedCapabilities(); !reflect.DeepEqual(got, want) {
		t.Fatalf("snapshot=%v want%v", got, want)
	}
	if got := h.Capabilities(); !reflect.DeepEqual(got, []string{"queue:read"}) {
		t.Fatalf("global snapshot widened: %v", got)
	}
	raw, err := json.Marshal(h.ScopedCapabilities())
	if err != nil {
		t.Fatal(err)
	}
	var wire []map[string]string
	if err = json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	if wire[0]["scope"] != "domain:a.test" || wire[0]["capability"] != "*" {
		t.Fatalf("wire=%s", raw)
	}
	got := h.ScopedCapabilities()
	got[0].Scope = All
	if reflect.DeepEqual(got, h.ScopedCapabilities()) {
		t.Fatal("snapshot aliases held state")
	}
}

func TestScopedIntersection(t *testing.T) {
	for _, tc := range []struct {
		name       string
		a, b, want []string
	}{
		{"global owner", []string{All}, []string{"domain:a.test"}, []string{"domain:a.test"}},
		{"global key", []string{"domain:a.test"}, []string{All}, []string{"domain:a.test"}},
		{"mailbox under domain", []string{"domain:a.test"}, []string{"mailbox:x@a.test"}, []string{"mailbox:x@a.test"}},
		{"disjoint tenants", []string{"domain:a.test"}, []string{"domain:b.test"}, nil},
		{"lookalike domains", []string{"domain:a.test"}, []string{"mailbox:x@evil-a.test"}, nil},
		{"mailbox never parent", []string{"mailbox:x@a.test"}, []string{"domain:a.test"}, []string{"mailbox:x@a.test"}},
		{"deduplicate", []string{All, All}, []string{"domain:a.test", "domain:a.test"}, []string{"domain:a.test"}},
		{"empty", nil, []string{All}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := IntersectScopes(tc.a, tc.b); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got%v want%v", got, tc.want)
			}
		})
	}
}
