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
			HeldOf(Entry{"domain:a.com", "mail:read"}), Held{}, nil},
		{"same capability, same scope",
			HeldOf(Entry{"domain:a.com", "mail:read"}), HeldOf(Entry{"domain:a.com", "mail:read"}),
			[]Entry{{"domain:a.com", "mail:read"}}},
		{"a capability the owner lacks is dropped",
			HeldOf(Entry{"domain:a.com", "mail:send"}), HeldOf(Entry{"domain:a.com", "mail:read"}), nil},
		{"wider than the owner narrows to the owner's scope",
			HeldOf(Entry{"*", "domains:read"}), HeldOf(Entry{"domain:a.com", "domains:read"}),
			[]Entry{{"domain:a.com", "domains:read"}}},
		{"narrower than the owner keeps its own scope",
			HeldOf(Entry{"mailbox:x@a.com", "mail:read"}), HeldOf(Entry{"domain:a.com", "mail:read"}),
			[]Entry{{"mailbox:x@a.com", "mail:read"}}},
		{"scopes that do not overlap give nothing",
			HeldOf(Entry{"domain:a.com", "mail:read"}), HeldOf(Entry{"domain:b.com", "mail:read"}), nil},
		{"the owner's wildcard keeps the held capability",
			HeldOf(Entry{"domain:a.com", "mail:read"}), HeldOf(Entry{"*", Wildcard}),
			[]Entry{{"domain:a.com", "mail:read"}}},
		{"a held wildcard becomes the owner's capabilities",
			HeldOf(Entry{"domain:a.com", Wildcard}), HeldOf(Entry{"*", "mail:read"}, Entry{"mailbox:x@a.com", "mail:send"}),
			[]Entry{{"domain:a.com", "mail:read"}, {"mailbox:x@a.com", "mail:send"}}},
		{"duplicates collapse",
			HeldOf(Entry{"domain:a.com", "mail:read"}, Entry{"*", "mail:read"}), HeldOf(Entry{"domain:a.com", "mail:read"}),
			[]Entry{{"domain:a.com", "mail:read"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.held.Within(c.ceiling).entries; !reflect.DeepEqual(got, c.want) {
				t.Errorf("Within = %v, want %v", got, c.want)
			}
		})
	}
}
