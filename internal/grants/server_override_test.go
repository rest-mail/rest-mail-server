package grants

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

var testVocabulary = []string{"domains:read", "mail:read", "mail:send", "mailboxes:write"}

func overrideServer(t *testing.T, raw string) {
	t.Helper()
	held, err := parseServerCapabilities(raw, testVocabulary)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	serverOverride.Store(&held)
	t.Cleanup(func() { serverOverride.Store(nil) })
}

// TestServerOverrideReplacesStoredCeiling: a configured set replaces the
// stored server object, so the database is never consulted (a nil handle
// would panic) and a capability the database would allow stays refused.
// The actor's own grants still bound the result.
func TestServerOverrideReplacesStoredCeiling(t *testing.T) {
	overrideServer(t, "mail:read, domains:read")
	actor := HeldOf(Entry{"mailbox:x@a.test", "mail:read"}, Entry{"mailbox:x@a.test", "mail:send"}, Entry{All, "mailboxes:write"})
	got, err := withinServer(nil, actor, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if want := []Entry{{"mailbox:x@a.test", "mail:read"}}; !reflect.DeepEqual(got.entries, want) {
		t.Fatalf("held = %v, want %v", got.entries, want)
	}
	if got.Allows("domains:read", All) {
		t.Fatal("server permission granted to an actor that lacks it")
	}
}

func TestServerOverrideEmptySetHoldsNothing(t *testing.T) {
	overrideServer(t, "  ")
	got, err := withinServer(nil, HeldOf(Entry{All, "mail:read"}, Entry{"domain:a.test", Wildcard}), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.entries) != 0 {
		t.Fatalf("empty override still allows %v", got.entries)
	}
}

func TestServerOverrideExemptsSuperadmin(t *testing.T) {
	overrideServer(t, "")
	superadmin := HeldOf(Entry{All, Wildcard})
	got, err := withinServer(nil, superadmin, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !got.Allows("mail:send", All) {
		t.Fatal("superadmin capped by the override")
	}
}

func TestServerOverrideCapsScopedWildcard(t *testing.T) {
	overrideServer(t, "mail:read")
	got, err := withinServer(nil, HeldOf(Entry{"domain:a.test", Wildcard}), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !got.Allows("mail:read", "mailbox:x@a.test") {
		t.Fatal("override removed the scoped actor's allowed capability")
	}
	if got.Allows("mail:send", "mailbox:x@a.test") || got.Allows("mail:read", "mailbox:x@b.test") || got.Allows("mail:read", All) {
		t.Fatal("scoped wildcard escaped the capability or scope ceiling")
	}
}

func TestParseServerCapabilities(t *testing.T) {
	got, err := parseServerCapabilities("mail:read,mail:send", testVocabulary)
	if err != nil {
		t.Fatal(err)
	}
	if want := []Entry{{All, "mail:read"}, {All, "mail:send"}}; !reflect.DeepEqual(got.entries, want) {
		t.Fatalf("entries = %v, want %v", got.entries, want)
	}
	for _, raw := range []string{"mail:reed", "*", "mail:read,*", "mail:read,,mail:send", "mail:read,"} {
		if _, err := parseServerCapabilities(raw, testVocabulary); err == nil || !strings.Contains(err.Error(), ServerCapabilitiesEnv) {
			t.Errorf("%q: err = %v, want refusal", raw, err)
		}
	}
}

// TestServerOverrideUnset: absent configuration clears any override and needs
// no database, leaving the stored server ceiling in charge.
func TestServerOverrideUnset(t *testing.T) {
	overrideServer(t, "mail:read")
	if err := ConfigureServerOverride(nil, "mail:send", false); err != nil {
		t.Fatal(err)
	}
	if serverOverride.Load() != nil {
		t.Fatal("unset configuration left an override in place")
	}
}
