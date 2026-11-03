package rfctest

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/rest-mail/go-imap/pkg/imap"
)

// hierarchyMailbox holds INBOX, a top-level folder and one inferior name, so
// LIST/LSUB wildcard and hierarchy behaviour can be observed on the wire.
type hierarchyMailbox struct{ mailbox }

func (hierarchyMailbox) Folders() ([]imap.Folder, error) {
	return []imap.Folder{{Name: "INBOX"}, {Name: "Work"}, {Name: "Work/Q1"}}, nil
}

// listNames returns the unquoted mailbox names of the untagged replies named
// kind.
func listNames(t *testing.T, kind string, untagged []string) []string {
	t.Helper()
	var names []string
	for _, l := range untagged {
		if !strings.HasPrefix(l, "* "+kind+" (") {
			t.Fatalf("untagged %q is not a %s reply", l, kind)
		}
		names = append(names, strings.Trim(l[strings.LastIndex(l, " ")+1:], `"`))
	}
	return names
}

// TestListLsubResponses covers RFC 3501 §§6.3.8, 6.3.9, 7.2.2 and 7.2.3: the
// empty-name delimiter request, the "*" and "%" wildcards, and that LSUB
// answers with untagged LSUB replies rather than LIST ones.
//
// checklist: IMAP-LIST-01 IMAP-LIST-02 IMAP-LIST-03 IMAP-LSUB-01
func TestListLsubResponses(t *testing.T) {
	for _, kind := range []string{"LIST", "LSUB"} {
		t.Run(kind, func(t *testing.T) {
			c := dialMailbox(t, hierarchyMailbox{})
			c.line()
			c.login()

			untagged, tagged := c.command("a1", `%s "" ""`, kind)
			if tagged != "a1 OK "+kind+" completed" {
				t.Fatalf(`%s "" "": tagged %q`, kind, tagged)
			}
			if len(untagged) != 1 || untagged[0] != `* `+kind+` (\Noselect) "/" ""` {
				t.Fatalf(`%s "" "": untagged %q, want the delimiter and an empty root name`, kind, untagged)
			}

			for _, tc := range []struct {
				pattern string
				want    string
			}{
				{"*", "INBOX Work Work/Q1"},
				{"%", "INBOX Work"},
				{"Work/%", "Work/Q1"},
				{"W*", "Work Work/Q1"},
				{"W%", "Work"},
				{"inbox", "INBOX"},
				{"Nothing*", ""},
			} {
				untagged, tagged := c.command("a2", `%s "" "%s"`, kind, tc.pattern)
				if tagged != "a2 OK "+kind+" completed" {
					t.Fatalf("%s %q: tagged %q", kind, tc.pattern, tagged)
				}
				if got := strings.Join(listNames(t, kind, untagged), " "); got != tc.want {
					t.Errorf("%s %q: names %q, want %q", kind, tc.pattern, got, tc.want)
				}
			}
		})
	}
}

// TestMailboxCommandsNeedAuthentication covers RFC 3501 §6.3: CREATE, DELETE,
// RENAME, SUBSCRIBE, UNSUBSCRIBE, LIST and LSUB are authenticated-state commands
// and are refused before login, with no state change.
//
// checklist: IMAP-MBOX-AUTH-01
func TestMailboxCommandsNeedAuthentication(t *testing.T) {
	c := dialMailbox(t, hierarchyMailbox{})
	c.line()
	for _, cmd := range []string{"CREATE Work2", "DELETE Work", "RENAME Work Play", "SUBSCRIBE Work", "UNSUBSCRIBE Work", `LIST "" "*"`, `LSUB "" "*"`} {
		untagged, tagged := c.command("p1", "%s", cmd)
		if len(untagged) != 0 || !strings.HasPrefix(tagged, "p1 NO ") {
			t.Errorf("%s before login: untagged %q, tagged %q, want tagged NO only", cmd, untagged, tagged)
		}
	}
	if c.backend.calls != 0 {
		t.Fatalf("backend authenticated %d times, want 0", c.backend.calls)
	}
}

// TestInboxCannotBeCreatedOrDeleted covers RFC 3501 §§6.3.3, 6.3.4: creating or
// deleting INBOX, in any case, is an error answered with tagged NO.
//
// checklist: IMAP-CREATE-01 IMAP-DELETE-01
// checklist: IMAP2-SEC-227
func TestInboxCannotBeCreatedOrDeleted(t *testing.T) {
	c := dialMailbox(t, hierarchyMailbox{})
	c.line()
	c.login()
	for _, name := range []string{"INBOX", "inbox", `"InBoX"`} {
		for _, cmd := range []string{"CREATE", "DELETE"} {
			if _, tagged := c.command("i1", "%s %s", cmd, name); !strings.HasPrefix(tagged, "i1 NO ") {
				t.Errorf("%s %s: tagged %q, want NO", cmd, name, tagged)
			}
		}
	}
	if _, tagged := c.command("i2", "CREATE Archive"); tagged != "i2 OK CREATE completed" {
		t.Fatalf("CREATE Archive control: tagged %q", tagged)
	}
}

// TestSubscriptionCommands covers RFC 3501 §§6.3.6, 6.3.7: SUBSCRIBE and
// UNSUBSCRIBE complete with OK in the authenticated and selected states, a
// missing name is BAD. There is no subscription store (IMAP-SUB-02): LSUB keeps
// listing every existing folder whatever was subscribed.
//
// checklist: IMAP-SUB-01 IMAP-LSUB-01
func TestSubscriptionCommands(t *testing.T) {
	c := dialMailbox(t, hierarchyMailbox{})
	c.line()
	c.login()
	for _, cmd := range []string{"SUBSCRIBE", "UNSUBSCRIBE"} {
		if _, tagged := c.command("s1", "%s Work", cmd); tagged != "s1 OK "+cmd+" completed" {
			t.Errorf("%s Work: tagged %q", cmd, tagged)
		}
		if _, tagged := c.command("s2", "%s Gone", cmd); tagged != "s2 OK "+cmd+" completed" {
			t.Errorf("%s of a nonexistent name: tagged %q", cmd, tagged)
		}
		if _, tagged := c.command("s3", `%s ""`, cmd); !strings.HasPrefix(tagged, "s3 BAD ") {
			t.Errorf(`%s "": tagged %q, want BAD`, cmd, tagged)
		}
	}
	untagged, _ := c.command("s4", `LSUB "" "*"`)
	if got := strings.Join(listNames(t, "LSUB", untagged), " "); got != "INBOX Work Work/Q1" {
		t.Errorf("LSUB after UNSUBSCRIBE: %q, want every folder", got)
	}
	if _, tagged := c.command("s5", "SELECT INBOX"); !strings.HasPrefix(tagged, "s5 OK ") {
		t.Fatalf("SELECT INBOX: %q", tagged)
	}
	if _, tagged := c.command("s6", "SUBSCRIBE Work"); tagged != "s6 OK SUBSCRIBE completed" {
		t.Errorf("SUBSCRIBE in selected state: tagged %q", tagged)
	}
}

// movingMailbox records Move calls; Work holds UID 7, Work/Q1 UID 8 and
// Workshop UID 9, a near name that is not an inferior of Work.
type movingMailbox struct {
	hierarchyMailbox
	mu    sync.Mutex
	moves []string
}

func (*movingMailbox) Folders() ([]imap.Folder, error) {
	return []imap.Folder{{Name: "INBOX"}, {Name: "Work"}, {Name: "Work/Q1"}, {Name: "Workshop"}}, nil
}

func (*movingMailbox) Messages(folder string) ([]imap.Message, error) {
	uid := map[string]uint32{"Work": 7, "Work/Q1": 8, "Workshop": 9}[folder]
	if uid == 0 {
		return nil, nil
	}
	return []imap.Message{{UID: uid, Size: 24}}, nil
}

func (m *movingMailbox) Move(uid uint32, dest string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.moves = append(m.moves, fmt.Sprintf("%d>%s", uid, dest))
	return nil
}

func (m *movingMailbox) takeMoves() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	sort.Strings(m.moves)
	out := strings.Join(m.moves, " ")
	m.moves = nil
	return out
}

// TestDeleteRenameHierarchy covers RFC 3501 §§6.3.4, 6.3.5: DELETE leaves
// inferior hierarchical names alone, and RENAME carries every inferior name
// with it while leaving a near name that only shares the prefix untouched.
//
// checklist: IMAP-DELETE-03 IMAP-RENAME-02
// checklist: IMAP2-CMD-88
func TestDeleteRenameHierarchy(t *testing.T) {
	box := &movingMailbox{}
	c := dialMailbox(t, box)
	c.line()
	c.login()

	if _, tagged := c.command("h1", "DELETE Work"); tagged != "h1 OK DELETE completed" {
		t.Fatalf("DELETE Work: tagged %q", tagged)
	}
	if got := box.takeMoves(); got != "7>Trash" {
		t.Errorf("DELETE Work moved %q, want only Work's own message (7>Trash)", got)
	}

	if _, tagged := c.command("h2", "RENAME Work Play"); tagged != "h2 OK RENAME completed" {
		t.Fatalf("RENAME Work Play: tagged %q", tagged)
	}
	if got := box.takeMoves(); got != "7>Play 8>Play/Q1" {
		t.Errorf("RENAME Work Play moved %q, want 7>Play 8>Play/Q1", got)
	}

	if _, tagged := c.command("h3", "RENAME Work/Q1 Q1"); tagged != "h3 OK RENAME completed" {
		t.Fatalf("RENAME Work/Q1 Q1: tagged %q", tagged)
	}
	if got := box.takeMoves(); got != "8>Q1" {
		t.Errorf("RENAME Work/Q1 Q1 moved %q, want 8>Q1", got)
	}
}
