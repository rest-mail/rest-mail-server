package grants

import (
	"reflect"
	"testing"
)

// TestWithin: an owned account holds only what its owner also holds, at the
// narrower of the two scopes (#341, Decided 12). A capability the owner lacks
// is dropped; a scope the owner holds only part of is narrowed to that part;
// the wildcard on either side stands for the other side's capability.
func TestWithin(t *testing.T) {
	cases := []struct {
		name          string
		held, ceiling Held
		want          []Entry
	}{
		{"nothing above holds nothing",
			HeldOf(Entry{Scope: "domain:a.com", Capability: "mail:read"}), Held{}, nil},
		{"same capability, same scope",
			HeldOf(Entry{Scope: "domain:a.com", Capability: "mail:read"}), HeldOf(Entry{Scope: "domain:a.com", Capability: "mail:read"}),
			[]Entry{{Scope: "domain:a.com", Capability: "mail:read"}}},
		{"a capability the owner lacks is dropped",
			HeldOf(Entry{Scope: "domain:a.com", Capability: "mail:send"}), HeldOf(Entry{Scope: "domain:a.com", Capability: "mail:read"}), nil},
		{"wider than the owner narrows to the owner's scope",
			HeldOf(Entry{Scope: "*", Capability: "domains:read"}), HeldOf(Entry{Scope: "domain:a.com", Capability: "domains:read"}),
			[]Entry{{Scope: "domain:a.com", Capability: "domains:read"}}},
		{"narrower than the owner keeps its own scope",
			HeldOf(Entry{Scope: "mailbox:x@a.com", Capability: "mail:read"}), HeldOf(Entry{Scope: "domain:a.com", Capability: "mail:read"}),
			[]Entry{{Scope: "mailbox:x@a.com", Capability: "mail:read"}}},
		{"scopes that do not overlap give nothing",
			HeldOf(Entry{Scope: "domain:a.com", Capability: "mail:read"}), HeldOf(Entry{Scope: "domain:b.com", Capability: "mail:read"}), nil},
		{"the owner's wildcard keeps the held capability",
			HeldOf(Entry{Scope: "domain:a.com", Capability: "mail:read"}), HeldOf(Entry{Scope: "*", Capability: Wildcard}),
			[]Entry{{Scope: "domain:a.com", Capability: "mail:read"}}},
		{"a held wildcard becomes the owner's capabilities",
			HeldOf(Entry{Scope: "domain:a.com", Capability: Wildcard}), HeldOf(Entry{Scope: "*", Capability: "mail:read"}, Entry{Scope: "mailbox:x@a.com", Capability: "mail:send"}),
			[]Entry{{Scope: "domain:a.com", Capability: "mail:read"}, {Scope: "mailbox:x@a.com", Capability: "mail:send"}}},
		{"duplicates collapse",
			HeldOf(Entry{Scope: "domain:a.com", Capability: "mail:read"}, Entry{Scope: "*", Capability: "mail:read"}), HeldOf(Entry{Scope: "domain:a.com", Capability: "mail:read"}),
			[]Entry{{Scope: "domain:a.com", Capability: "mail:read"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.held.Within(c.ceiling).entries; !reflect.DeepEqual(got, c.want) {
				t.Errorf("Within = %v, want %v", got, c.want)
			}
		})
	}
}
