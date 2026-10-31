package grants

import (
	"testing"
	"time"
)

// An administrator at "*" who also owns a personal mailbox, as grants.Load
// reads them: the admin role's capabilities with admin:session, and the
// owner's capabilities with mailbox:session on its own mailbox.
func sessionTestAdmin() Held {
	own := "mailbox:admin@a.test"
	return HeldOf(
		Entry{All, "domains:read"}, Entry{All, "admin:session"},
		Entry{own, "mail:read"}, Entry{own, "mail:send"}, Entry{own, "mailbox:session"},
	)
}

// TestMailboxSessionRemovedFromServer (#324): without mailbox:session on the
// server, an administrator loses its personal mailbox, but keeps its
// administrative capabilities and may still sign in as an administrator.
func TestMailboxSessionRemovedFromServer(t *testing.T) {
	overrideServer(t, "domains:read, mail:read, mail:send, admin:session")
	got, err := withinServer(nil, sessionTestAdmin(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got.Allows("mail:read", "mailbox:admin@a.test") || got.Allows("mail:send", "mailbox:admin@a.test") {
		t.Fatal("being an administrator bypassed the mailbox session restriction")
	}
	if got.MailboxSession("admin@a.test") {
		t.Fatal("mailbox session allowed with mailbox:session removed from the server")
	}
	if !got.Allows("domains:read", All) || !got.AdminSession() {
		t.Fatal("administrative access lost with only mailbox:session removed")
	}
}

// TestAdminSessionRemovedFromServer: without admin:session on the server,
// administrative capabilities stop reaching anything, and the account's own
// mailbox stays reachable.
func TestAdminSessionRemovedFromServer(t *testing.T) {
	overrideServer(t, "domains:read, mail:read, mail:send, mailbox:session")
	got, err := withinServer(nil, sessionTestAdmin(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got.Allows("domains:read", All) || got.AdminSession() {
		t.Fatal("administrative access allowed with admin:session removed from the server")
	}
	if !got.Allows("mail:read", "mailbox:admin@a.test") || !got.MailboxSession("admin@a.test") {
		t.Fatal("personal mailbox lost with only admin:session removed")
	}
}

// TestSessionNeedsTheActorsOwnCapability: the server holding a session
// capability does not give it to an actor that lacks it.
func TestSessionNeedsTheActorsOwnCapability(t *testing.T) {
	overrideServer(t, "domains:read, mail:read, admin:session, mailbox:session")
	actor := HeldOf(Entry{All, "domains:read"}, Entry{"mailbox:x@a.test", "mail:read"})
	got, err := withinServer(nil, actor, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got.Allows("domains:read", All) || got.Allows("mail:read", "mailbox:x@a.test") {
		t.Fatalf("server session capability granted to an actor without it: %v", got.entries)
	}
}

// TestSessionScopeMustCover: a session capability held on one mailbox does
// not open another.
func TestSessionScopeMustCover(t *testing.T) {
	got := HeldOf(
		Entry{"mailbox:x@a.test", "mailbox:session"},
		Entry{"mailbox:x@a.test", "mail:read"}, Entry{"mailbox:y@a.test", "mail:read"},
	).withinSessions()
	if !got.Allows("mail:read", "mailbox:x@a.test") || got.Allows("mail:read", "mailbox:y@a.test") {
		t.Fatalf("session scope not applied: %v", got.entries)
	}
}

// TestSuperadminKeepsSessionsWhateverTheServer: the recovery exception.
func TestSuperadminKeepsSessionsWhateverTheServer(t *testing.T) {
	overrideServer(t, "")
	got, err := withinServer(nil, HeldOf(Entry{All, Wildcard}), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !got.AdminSession() || !got.MailboxSession("root@a.test") || !got.Allows("mail:read", "mailbox:root@a.test") {
		t.Fatal("superadmin capped by the session capabilities")
	}
}
