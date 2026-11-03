package rfctest

import (
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/rest-mail/go-imap/pkg/imap"
)

// subscribingMailbox keeps a subscription list ([imap.Subscriber]) beside a
// fixed hierarchy, so SUBSCRIBE and UNSUBSCRIBE change what LSUB and LIST's
// SUBSCRIBED options answer.
type subscribingMailbox struct {
	mailbox
	folders []string
	mu      sync.Mutex
	subs    []string
	err     error
}

func newSubscribingMailbox(folders ...string) *subscribingMailbox {
	return &subscribingMailbox{folders: folders}
}

func (m *subscribingMailbox) Folders() ([]imap.Folder, error) {
	out := make([]imap.Folder, len(m.folders))
	for i, name := range m.folders {
		out[i] = imap.Folder{Name: name}
	}
	return out, nil
}

func (m *subscribingMailbox) Subscriptions() ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.subs), m.err
}

func (m *subscribingMailbox) Subscribe(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	if !slices.Contains(m.subs, name) {
		m.subs = append(m.subs, name)
	}
	return nil
}

func (m *subscribingMailbox) Unsubscribe(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.subs = slices.DeleteFunc(m.subs, func(s string) bool { return s == name })
	return nil
}

// subscribe sends SUBSCRIBE for each name, or UNSUBSCRIBE when cmd says so.
func subscribe(t *testing.T, c *client, cmd string, names ...string) {
	t.Helper()
	for _, name := range names {
		_, tagged := c.command("S1", `%s "%s"`, cmd, name)
		requireOK(t, tagged, "S1")
	}
}

// rfc5258Hierarchy is the local hierarchy of RFC 5258 §5 example 1.
func rfc5258Hierarchy() *subscribingMailbox {
	return newSubscribingMailbox("INBOX", "Fruit", "Fruit/Apple", "Fruit/Banana", "Tofu", "Vegetable", "Vegetable/Broccoli", "Vegetable/Corn")
}

// TestListExtendedSubscriptionExamples replays RFC 5258 §5 examples 2, 3, 5
// and 6 against a store that keeps subscriptions. "Fruit/Peach" is subscribed
// but does not exist ("perhaps it was deleted while still subscribed"), so LIST
// (SUBSCRIBED) answers it \Subscribed \NonExistent, while RETURN (SUBSCRIBED)
// does not add it (example 6: "return options MUST NOT cause the server to
// report information about additional mailbox names", §3). The order of the
// replies, and of attributes in one reply, is the server's; the RFC gives it
// no meaning.
//
// checklist: IMAP-LISTEXT-03 IMAP-LISTEXT-09 IMAP-LISTEXT-12
func TestListExtendedSubscriptionExamples(t *testing.T) {
	c := selectionClient(t, rfc5258Hierarchy())
	subscribe(t, c, "SUBSCRIBE", "INBOX", "Fruit/Banana", "Fruit/Peach", "Vegetable", "Vegetable/Broccoli")

	subscribed := []string{
		`* LIST (\Subscribed) "/" "INBOX"`, `* LIST (\Subscribed) "/" "Fruit/Banana"`,
		`* LIST (\Subscribed) "/" "Vegetable"`, `* LIST (\Subscribed) "/" "Vegetable/Broccoli"`,
		`* LIST (\Subscribed \NonExistent) "/" "Fruit/Peach"`,
	}
	runExactly(t, c, `LIST (SUBSCRIBED) "" "*"`, subscribed...)        // example 2
	runExactly(t, c, `LIST (REMOTE SUBSCRIBED) "" "*"`, subscribed...) // example 5, no remote mailboxes
	runExactly(t, c, `LIST () "" "%" RETURN (CHILDREN)`,               // example 3
		`* LIST (\HasNoChildren) "/" "INBOX"`, `* LIST (\HasChildren) "/" "Fruit"`,
		`* LIST (\HasNoChildren) "/" "Tofu"`, `* LIST (\HasChildren) "/" "Vegetable"`)
	runExactly(t, c, `LIST (REMOTE) "" "*" RETURN (SUBSCRIBED)`, // example 6
		`* LIST (\Subscribed) "/" "INBOX"`, `* LIST () "/" "Fruit"`, `* LIST () "/" "Fruit/Apple"`,
		`* LIST (\Subscribed) "/" "Fruit/Banana"`, `* LIST () "/" "Tofu"`, `* LIST (\Subscribed) "/" "Vegetable"`,
		`* LIST (\Subscribed) "/" "Vegetable/Broccoli"`, `* LIST () "/" "Vegetable/Corn"`)

	// "Fruit" is not subscribed and is not returned without RECURSIVEMATCH; a
	// name that does not exist has no children and gets no STATUS (RFC 5819
	// §2).
	runExactly(t, c, `LIST (SUBSCRIBED) "" "%"`, `* LIST (\Subscribed) "/" "INBOX"`, `* LIST (\Subscribed) "/" "Vegetable"`)
	runExactly(t, c, `LIST (SUBSCRIBED) "" "Fruit/*" RETURN (CHILDREN STATUS (MESSAGES))`,
		`* LIST (\Subscribed \HasNoChildren) "/" "Fruit/Banana"`, `* STATUS "Fruit/Banana" (MESSAGES 0)`,
		`* LIST (\Subscribed \NonExistent \HasNoChildren) "/" "Fruit/Peach"`)

	subscribe(t, c, "UNSUBSCRIBE", "Fruit/Peach", "Fruit/Peach")
	runExactly(t, c, `LIST (SUBSCRIBED) "" "Fruit/*"`, `* LIST (\Subscribed) "/" "Fruit/Banana"`)
}

// TestLsubFromSubscriptions: LSUB "returns a subset of names from the set of
// names that the user has declared as being "active" or "subscribed"", a name
// that does not exist among them; a level that is not subscribed but has a
// subscribed name below it is answered \Noselect when "%" ends the pattern
// (RFC 3501 §6.3.9). LIST (SUBSCRIBED) never gives \Noselect that meaning
// (RFC 5258 §3.5).
//
// checklist: IMAP-LISTEXT-09
func TestLsubFromSubscriptions(t *testing.T) {
	c := selectionClient(t, rfc5258Hierarchy())
	subscribe(t, c, "SUBSCRIBE", "inbox", "Fruit/Banana", "Fruit/Peach", "Vegetable")
	runExactly(t, c, `LSUB "" "*"`,
		`* LSUB () "/" "INBOX"`, `* LSUB () "/" "Fruit/Banana"`, `* LSUB () "/" "Vegetable"`, `* LSUB () "/" "Fruit/Peach"`)
	runExactly(t, c, `LSUB "" "%"`, `* LSUB () "/" "INBOX"`, `* LSUB (\Noselect) "/" "Fruit"`, `* LSUB () "/" "Vegetable"`)
	runExactly(t, c, `LIST (SUBSCRIBED RECURSIVEMATCH) "" "%"`,
		`* LIST (\Subscribed) "/" "INBOX"`, `* LIST () "/" "Fruit" ("CHILDINFO" ("SUBSCRIBED"))`, `* LIST (\Subscribed) "/" "Vegetable"`)
	subscribe(t, c, "UNSUBSCRIBE", "INBOX", "Fruit/Banana", "Fruit/Peach", "Vegetable")
	runExactly(t, c, `LSUB "" "*"`)
}

// TestListExtendedChildInfoExamples replays RFC 5258 §5 example 8: CHILDINFO
// says a descendant meets the selection, \HasChildren that one exists. Case A2
// uses a hierarchy without "Foo".
//
// checklist: IMAP-LISTEXT-04 IMAP-LISTEXT-12
func TestListExtendedChildInfoExamples(t *testing.T) {
	c := selectionClient(t, newSubscribingMailbox("INBOX", "Foo", "Foo/Bar", "Foo/Baz", "Moo"))
	runExactly(t, c, `LIST "" "%" RETURN (CHILDREN)`,
		`* LIST (\HasNoChildren) "/" "INBOX"`, `* LIST (\HasChildren) "/" "Foo"`, `* LIST (\HasNoChildren) "/" "Moo"`)

	// A) Only "Foo/Baz" is subscribed.
	subscribe(t, c, "SUBSCRIBE", "Foo/Baz")
	runExactly(t, c, `LIST (SUBSCRIBED) "" "*"`, `* LIST (\Subscribed) "/" "Foo/Baz"`)
	runExactly(t, c, `LIST (SUBSCRIBED) "" "%"`)
	runExactly(t, c, `LIST (SUBSCRIBED RECURSIVEMATCH) "" "%"`, `* LIST () "/" "Foo" ("CHILDINFO" ("SUBSCRIBED"))`)
	// A1) "Foo" is subscribed as well.
	subscribe(t, c, "SUBSCRIBE", "Foo")
	runExactly(t, c, `LIST (SUBSCRIBED RECURSIVEMATCH) "" "%"`, `* LIST (\Subscribed) "/" "Foo" ("CHILDINFO" ("SUBSCRIBED"))`)
	runExactly(t, c, `LIST (SUBSCRIBED RECURSIVEMATCH) "" "%" RETURN (CHILDREN)`,
		`* LIST (\Subscribed \HasChildren) "/" "Foo" ("CHILDINFO" ("SUBSCRIBED"))`)
	// C) Only "Foo" and "Moo" are subscribed.
	subscribe(t, c, "UNSUBSCRIBE", "Foo/Baz")
	subscribe(t, c, "SUBSCRIBE", "Moo")
	runExactly(t, c, `LIST (SUBSCRIBED RECURSIVEMATCH) "" "%" RETURN (CHILDREN)`,
		`* LIST (\Subscribed \HasChildren) "/" "Foo"`, `* LIST (\Subscribed \HasNoChildren) "/" "Moo"`)
	// B) Nothing is subscribed.
	subscribe(t, c, "UNSUBSCRIBE", "Foo", "Moo")
	runExactly(t, c, `LIST (SUBSCRIBED RECURSIVEMATCH) "" "%"`)

	// A2) "Foo" is not part of the hierarchy and is not subscribed.
	c = selectionClient(t, newSubscribingMailbox("INBOX", "Foo/Bar", "Foo/Baz", "Moo"))
	subscribe(t, c, "SUBSCRIBE", "Foo/Baz")
	runExactly(t, c, `LIST (SUBSCRIBED RECURSIVEMATCH) "" "%"`, `* LIST (\NonExistent) "/" "Foo" ("CHILDINFO" ("SUBSCRIBED"))`)
}

// TestListExtendedChildInfoPattern replays RFC 5258 §5 example 9: CHILDINFO is
// returned whether or not the children match the pattern, and suppressed where
// every subscribed child is returned anyway ("servers SHOULD suppress
// redundant CHILDINFO responses", §3.5), as for "baz2" and "qux2".
//
// checklist: IMAP-LISTEXT-04 IMAP-LISTEXT-12
func TestListExtendedChildInfoPattern(t *testing.T) {
	c := selectionClient(t, newSubscribingMailbox("INBOX", "foo2", "foo2/bar1", "foo2/bar2", "baz2", "baz2/bar2",
		"baz2/bar22", "baz2/bar222", "eps2", "eps2/mamba", "qux2/bar2"))
	subscribe(t, c, "SUBSCRIBE", "foo2/bar1", "foo2/bar2", "baz2/bar2", "baz2/bar22", "baz2/bar222", "eps2", "eps2/mamba", "qux2/bar2")
	runExactly(t, c, `LIST (SUBSCRIBED) "" "*"`,
		`* LIST (\Subscribed) "/" "foo2/bar1"`, `* LIST (\Subscribed) "/" "foo2/bar2"`, `* LIST (\Subscribed) "/" "baz2/bar2"`,
		`* LIST (\Subscribed) "/" "baz2/bar22"`, `* LIST (\Subscribed) "/" "baz2/bar222"`, `* LIST (\Subscribed) "/" "eps2"`,
		`* LIST (\Subscribed) "/" "eps2/mamba"`, `* LIST (\Subscribed) "/" "qux2/bar2"`)
	runExactly(t, c, `LIST (RECURSIVEMATCH SUBSCRIBED) "" "*2"`,
		`* LIST () "/" "foo2" ("CHILDINFO" ("SUBSCRIBED"))`, `* LIST (\Subscribed) "/" "foo2/bar2"`,
		`* LIST (\Subscribed) "/" "baz2/bar2"`, `* LIST (\Subscribed) "/" "baz2/bar22"`, `* LIST (\Subscribed) "/" "baz2/bar222"`,
		`* LIST (\Subscribed) "/" "eps2" ("CHILDINFO" ("SUBSCRIBED"))`, `* LIST (\Subscribed) "/" "qux2/bar2"`)
}

// TestListExtendedMissingChild replays RFC 5258 §5 example 10: "foo/bar" is
// subscribed and does not exist, so "foo" has CHILDINFO and yet
// \HasNoChildren; several patterns answer "foo" once.
//
// checklist: IMAP-LISTEXT-05 IMAP-LISTEXT-12
func TestListExtendedMissingChild(t *testing.T) {
	c := selectionClient(t, newSubscribingMailbox("foo"))
	subscribe(t, c, "SUBSCRIBE", "foo/bar")
	runExactly(t, c, `LIST "" ("foo" "foo/*")`, `* LIST () "/" "foo"`)
	runExactly(t, c, `LIST (SUBSCRIBED) "" "foo/*"`, `* LIST (\Subscribed \NonExistent) "/" "foo/bar"`)
	runExactly(t, c, `LIST (SUBSCRIBED RECURSIVEMATCH) "" foo RETURN (CHILDREN)`,
		`* LIST (\HasNoChildren) "/" "foo" ("CHILDINFO" ("SUBSCRIBED"))`)
}

// TestSubscriptionStoreFailure: a store that cannot read or change its list
// answers NO, and the list is not guessed.
//
// checklist: IMAP-LISTEXT-09
func TestSubscriptionStoreFailure(t *testing.T) {
	box := rfc5258Hierarchy()
	box.err = errors.New("store down")
	c := selectionClient(t, box)
	for _, command := range []string{`SUBSCRIBE "Tofu"`, `UNSUBSCRIBE "Tofu"`, `LSUB "" "*"`, `LIST (SUBSCRIBED) "" "*"`, `LIST "" "*" RETURN (SUBSCRIBED)`} {
		if lines, tagged := c.command("F1", "%s", command); !strings.HasPrefix(tagged, "F1 NO") || len(lines) != 0 {
			t.Fatalf("%s: %q %q, want NO", command, lines, tagged)
		}
	}
	// A LIST that needs no subscriptions does not read them.
	runExactly(t, c, `LIST "" "Tofu"`, `* LIST () "/" "Tofu"`)
}
