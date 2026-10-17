package grants

import (
	"reflect"
	"testing"
)

func TestParseScope(t *testing.T) {
	good := map[string]string{
		"*":                            "*",
		" * ":                          "*",
		"domain:a.com":                 "domain:a.com",
		"domain:Mail.Example.COM":      "domain:mail.example.com",
		"domain:xn--bcher-kva.test":    "domain:xn--bcher-kva.test",
		"mailbox:Chris@A.com":          "mailbox:chris@a.com",
		"mailbox:first.last+tag@a.com": "mailbox:first.last+tag@a.com",
	}
	for in, want := range good {
		got, err := ParseScope(in)
		if err != nil || got != want {
			t.Errorf("ParseScope(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{
		"", "**", "all", "domain:", "domain:a..com", "domain:a_b.com", "domain:a.com/x",
		"domain:chris@a.com", "mailbox:a.com", "mailbox:@a.com", "mailbox:chris@", "mailbox:a@b@c.com",
		"mailbox:a b@c.com", "folder:a@b.com/Inbox", "Domain a.com",
	} {
		if got, err := ParseScope(bad); err == nil {
			t.Errorf("ParseScope(%q) = %q, want an error", bad, got)
		}
	}
}

func TestCovers(t *testing.T) {
	cases := []struct {
		scope, target string
		want          bool
	}{
		{"*", "*", true},
		{"*", "domain:a.com", true},
		{"*", "mailbox:x@a.com", true},
		{"domain:a.com", "domain:a.com", true},
		{"domain:a.com", "mailbox:x@a.com", true},
		{"domain:a.com", "*", false},
		{"domain:a.com", "domain:b.com", false},
		{"domain:a.com", "mailbox:x@b.com", false},
		// Not a suffix match: a.com does not cover mail.a.com or evila.com.
		{"domain:a.com", "domain:mail.a.com", false},
		{"domain:a.com", "mailbox:x@mail.a.com", false},
		{"domain:a.com", "mailbox:x@evila.com", false},
		{"mailbox:x@a.com", "mailbox:x@a.com", true},
		{"mailbox:x@a.com", "mailbox:y@a.com", false},
		{"mailbox:x@a.com", "domain:a.com", false},
		{"mailbox:x@a.com", "*", false},
	}
	for _, c := range cases {
		if got := Covers(c.scope, c.target); got != c.want {
			t.Errorf("Covers(%q, %q) = %v, want %v", c.scope, c.target, got, c.want)
		}
	}
}

func TestHeld(t *testing.T) {
	admin := HeldOf(
		Entry{All, "domains:read"},
		Entry{All, "domains:write"},
		Entry{"domain:a.com", "mailboxes:write"},
	)
	if !admin.Allows("domains:read", All) || !admin.Allows("domains:read", "domain:b.com") {
		t.Error("a capability at * should be allowed everywhere")
	}
	if admin.Allows("mailboxes:write", All) {
		t.Error("a capability held on one domain should not be allowed at *")
	}
	if !admin.Allows("mailboxes:write", "mailbox:x@a.com") || admin.Allows("mailboxes:write", "mailbox:x@b.com") {
		t.Error("a capability held on a.com should cover a.com's mailboxes only")
	}
	if !admin.AllowsSomewhere("mailboxes:write") || admin.AllowsSomewhere("users:write") {
		t.Error("AllowsSomewhere wrong")
	}
	// Only what is held at * is listed, sorted.
	if got, want := admin.Capabilities(), []string{"domains:read", "domains:write"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Capabilities() = %v, want %v", got, want)
	}

	super := HeldOf(Entry{All, "domains:read"}, Entry{All, Wildcard})
	if !super.Allows("anything:at_all", "mailbox:x@a.com") || !super.Allows(Wildcard, All) {
		t.Error("* at * should allow everything")
	}
	if got := super.Capabilities(); !reflect.DeepEqual(got, []string{Wildcard}) {
		t.Errorf("superadmin Capabilities() = %v, want [*]", got)
	}
	if got := (Held{}).Capabilities(); len(got) != 0 {
		t.Errorf("no grants: Capabilities() = %v, want none", got)
	}

	// * held on one domain is everything there, and nothing anywhere else.
	domainSuper := HeldOf(Entry{"domain:a.com", Wildcard})
	if !domainSuper.Allows("users:write", "domain:a.com") || domainSuper.Allows("users:write", All) {
		t.Error("* at a domain should be bounded by the domain")
	}
	if got := domainSuper.Capabilities(); len(got) != 0 {
		t.Errorf("Capabilities() = %v: * on a domain is not * at *", got)
	}
}

func TestCanGrant(t *testing.T) {
	adminCaps := []string{"domains:read", "domains:write", "mailboxes:read"}
	superadmin := HeldOf(Entry{All, Wildcard})
	admin := HeldOf(Entry{All, "domains:read"}, Entry{All, "domains:write"}, Entry{All, "mailboxes:read"}, Entry{All, "users:write"})
	domainAdmin := HeldOf(Entry{"domain:a.com", "domains:read"}, Entry{"domain:a.com", "domains:write"}, Entry{"domain:a.com", "mailboxes:read"})

	cases := []struct {
		name   string
		held   Held
		caps   []string
		scope  string
		expect bool
	}{
		{"superadmin grants anything anywhere", superadmin, []string{Wildcard}, All, true},
		{"superadmin grants a role on a domain", superadmin, adminCaps, "domain:a.com", true},
		{"admin grants what it holds at *", admin, adminCaps, All, true},
		{"admin grants what it holds on a domain", admin, adminCaps, "domain:b.com", true},
		{"admin cannot grant superadmin", admin, []string{Wildcard}, All, false},
		{"admin cannot grant a capability it lacks", admin, append(adminCaps, "bans:write"), All, false},
		{"domain admin grants within its domain", domainAdmin, adminCaps, "domain:a.com", true},
		{"domain admin grants on a mailbox of its domain", domainAdmin, adminCaps, "mailbox:x@a.com", true},
		{"domain admin cannot grant at *", domainAdmin, adminCaps, All, false},
		{"domain admin cannot grant on another domain", domainAdmin, adminCaps, "domain:b.com", false},
		{"nobody grants * from nothing", Held{}, []string{Wildcard}, All, false},
	}
	for _, c := range cases {
		if got := c.held.CanGrant(c.caps, c.scope); got != c.expect {
			t.Errorf("%s: CanGrant = %v, want %v", c.name, got, c.expect)
		}
	}
}
