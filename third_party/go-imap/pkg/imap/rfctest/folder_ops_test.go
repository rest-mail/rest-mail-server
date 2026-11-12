package rfctest

import (
	"errors"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/rest-mail/go-imap/pkg/imap"
)

// folderStore keeps folders apart from the messages in them and says so
// through imap.FolderMailbox, so CREATE, DELETE and RENAME change what LIST
// reports.
type folderStore struct {
	mailbox
	mu      sync.Mutex
	folders map[string][]imap.Message
}

func newFolderStore(folders map[string][]imap.Message) *folderStore {
	return &folderStore{folders: folders}
}

func (m *folderStore) Folders() ([]imap.Folder, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	names := make([]string, 0, len(m.folders))
	for name := range m.folders {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]imap.Folder, 0, len(names))
	for _, name := range names {
		out = append(out, imap.Folder{Name: name})
	}
	return out, nil
}

func (m *folderStore) Messages(folder string) ([]imap.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	msgs, ok := m.folders[folder]
	if !ok {
		return nil, imap.ErrNoSuchMailbox
	}
	return slices.Clone(msgs), nil
}

func (m *folderStore) CreateFolder(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.folders[name]; ok {
		return errors.New("exists")
	}
	m.folders[name] = nil
	return nil
}

func (m *folderStore) DeleteFolder(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.folders[name]; !ok {
		return errors.New("no such folder")
	}
	delete(m.folders, name)
	return nil
}

func (m *folderStore) RenameFolder(from, to string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	msgs, ok := m.folders[from]
	if !ok {
		return errors.New("no such folder")
	}
	if _, ok := m.folders[to]; ok {
		return errors.New("exists")
	}
	m.folders[to] = msgs
	if from == "INBOX" {
		m.folders[from] = nil
	} else {
		delete(m.folders, from)
	}
	return nil
}

func (m *folderStore) names() string {
	folders, _ := m.Folders()
	var names []string
	for _, f := range folders {
		names = append(names, f.Name)
	}
	return strings.Join(names, " ")
}

// listed is the names LIST "" "*" reports, in order.
func listed(t *testing.T, c *client) string {
	t.Helper()
	untagged, tagged := c.command("ls", `LIST "" "*"`)
	requireOK(t, tagged, "ls")
	return strings.Join(listNames(t, "LIST", untagged), " ")
}

// TestCreateMakesAnEmptyFolder covers RFC 9051 §6.3.4 with a store that
// creates folders: CREATE answers OK only once the folder exists, so the next
// LIST shows it while it is still empty; creating an extant name is NO
// [ALREADYEXISTS]; a trailing delimiter is dropped and the superiors a name
// needs are created first.
//
// checklist: IMAP-CREATE-02 IMAP-CREATE-03 IMAP-INV-CREATE-01 IMAP-INV-CREATE-02
// checklist: IMAP2-CMD-79 IMAP2-CMD-81 IMAP2-CMD-82
func TestCreateMakesAnEmptyFolder(t *testing.T) {
	store := newFolderStore(map[string][]imap.Message{"INBOX": {{UID: 1}}})
	c := dialMailbox(t, store)
	c.line()
	c.login()

	if _, tagged := c.command("c1", "CREATE Projects"); tagged != "c1 OK CREATE completed" {
		t.Fatalf("CREATE Projects: tagged %q", tagged)
	}
	if got := listed(t, c); got != "INBOX Projects" {
		t.Fatalf("LIST after CREATE: %q, want the empty folder listed", got)
	}
	untagged, tagged := c.command("c2", "STATUS Projects (MESSAGES)")
	requireOK(t, tagged, "c2")
	if len(untagged) != 1 || !strings.HasSuffix(untagged[0], `(MESSAGES 0)`) {
		t.Fatalf("STATUS of the new folder: %q", untagged)
	}
	if _, tagged := c.command("c3", "CREATE Projects"); tagged != "c3 NO [ALREADYEXISTS] Mailbox already exists" {
		t.Fatalf("CREATE of an extant name: tagged %q", tagged)
	}
	if _, tagged := c.command("c4", "CREATE Clients/Acme/"); tagged != "c4 OK CREATE completed" {
		t.Fatalf("CREATE Clients/Acme/: tagged %q", tagged)
	}
	if got := store.names(); got != "Clients Clients/Acme INBOX Projects" {
		t.Fatalf("store after CREATE Clients/Acme/: %q, want the superior and the name without its trailing delimiter", got)
	}
}

// TestDeleteRemovesTheFolder covers RFC 9051 §6.3.5 with a store that deletes
// folders: DELETE removes the folder for good and leaves its inferiors, a name
// that does not exist is NO [NONEXISTENT], and a \Noselect name with inferiors
// is NO [HASCHILDREN].
//
// checklist: IMAP-DELETE-02 IMAP-DELETE-04 IMAP2-CMD-84
func TestDeleteRemovesTheFolder(t *testing.T) {
	store := newFolderStore(map[string][]imap.Message{
		"INBOX": nil, "Trash": nil, "Work": {{UID: 5}}, "Work/Q1": {{UID: 6}}, "Old/2019": nil,
	})
	c := dialMailbox(t, store)
	c.line()
	c.login()

	if _, tagged := c.command("d1", "DELETE Work"); tagged != "d1 OK DELETE completed" {
		t.Fatalf("DELETE Work: tagged %q", tagged)
	}
	if got := listed(t, c); got != `INBOX Old/2019 Trash Work/Q1` {
		t.Fatalf("LIST after DELETE Work: %q", got)
	}
	if _, ok := store.folders["Work"]; ok {
		t.Fatal("DELETE Work left the folder in the store")
	}
	untagged, _ := c.command("d2", `LIST "" Work`)
	if len(untagged) != 1 || !strings.Contains(untagged[0], `\Noselect`) {
		t.Fatalf("LIST Work after DELETE: %q, want \\Noselect for the superior of Work/Q1", untagged)
	}
	if store.folders["Trash"] != nil {
		t.Fatalf("DELETE moved messages to Trash: %v", store.folders["Trash"])
	}
	if _, tagged := c.command("d3", "DELETE Gone"); tagged != "d3 NO [NONEXISTENT] No such mailbox" {
		t.Fatalf("DELETE of a name that does not exist: tagged %q", tagged)
	}
	if _, tagged := c.command("d4", "DELETE Old"); tagged != "d4 NO [HASCHILDREN] Mailbox has inferior mailboxes" {
		t.Fatalf("DELETE of a \\Noselect name with inferiors: tagged %q", tagged)
	}
}

// TestRenameMovesInferiors covers RFC 9051 §6.3.6 with a store that renames
// folders: the folder and its inferiors take the new name, a near name only
// sharing the prefix is untouched, superiors of the new name are created, a
// source that does not exist is NO [NONEXISTENT] and a destination that does
// is NO [ALREADYEXISTS].
//
// checklist: IMAP-RENAME-01 IMAP2-CMD-89
func TestRenameMovesInferiors(t *testing.T) {
	store := newFolderStore(map[string][]imap.Message{
		"INBOX": nil, "Work": {{UID: 5}}, "Work/Q1": {{UID: 6}}, "Work/Q1/Jan": nil, "Workshop": nil,
	})
	c := dialMailbox(t, store)
	c.line()
	c.login()

	if _, tagged := c.command("r1", "RENAME Work Archive/Play"); tagged != "r1 OK RENAME completed" {
		t.Fatalf("RENAME Work Archive/Play: tagged %q", tagged)
	}
	if got := listed(t, c); got != "Archive Archive/Play Archive/Play/Q1 Archive/Play/Q1/Jan INBOX Workshop" {
		t.Fatalf("LIST after RENAME: %q", got)
	}
	if msgs := store.folders["Archive/Play/Q1"]; len(msgs) != 1 || msgs[0].UID != 6 {
		t.Fatalf("Archive/Play/Q1 holds %v, want the inferior's message", msgs)
	}
	if _, tagged := c.command("r2", "RENAME Gone Elsewhere"); tagged != "r2 NO [NONEXISTENT] No such mailbox" {
		t.Fatalf("RENAME from a name that does not exist: tagged %q", tagged)
	}
	if _, tagged := c.command("r3", "RENAME Workshop Archive"); tagged != "r3 NO [ALREADYEXISTS] Mailbox already exists" {
		t.Fatalf("RENAME to a name that exists: tagged %q", tagged)
	}
	if _, tagged := c.command("r4", "RENAME Workshop inbox"); tagged != "r4 NO [ALREADYEXISTS] Mailbox already exists" {
		t.Fatalf("RENAME to INBOX: tagged %q", tagged)
	}
	if _, tagged := c.command("r5", "RENAME Workshop Workshop/Inner"); !strings.HasPrefix(tagged, "r5 NO ") {
		t.Fatalf("RENAME into itself: tagged %q", tagged)
	}
	if got := store.names(); got != "Archive Archive/Play Archive/Play/Q1 Archive/Play/Q1/Jan INBOX Workshop" {
		t.Fatalf("refused renames changed the store: %q", got)
	}
}

// TestRenameInboxMovesItsMessages covers RFC 9051 §6.3.6: renaming INBOX moves
// all its messages to the new name and leaves INBOX in place and empty; its
// inferiors are not renamed.
//
// checklist: IMAP-RENAME-03
func TestRenameInboxMovesItsMessages(t *testing.T) {
	store := newFolderStore(map[string][]imap.Message{
		"INBOX": {{UID: 1}, {UID: 2}}, "INBOX/Receipts": {{UID: 3}},
	})
	c := dialMailbox(t, store)
	c.line()
	c.login()

	if _, tagged := c.command("i1", "RENAME inbox Old-Mail"); tagged != "i1 OK RENAME completed" {
		t.Fatalf("RENAME INBOX: tagged %q", tagged)
	}
	if got := listed(t, c); got != "INBOX INBOX/Receipts Old-Mail" {
		t.Fatalf("LIST after RENAME INBOX: %q", got)
	}
	for folder, want := range map[string]string{"INBOX": "0", "Old-Mail": "2", "INBOX/Receipts": "1"} {
		untagged, tagged := c.command("i2", "STATUS %s (MESSAGES)", folder)
		requireOK(t, tagged, "i2")
		if len(untagged) != 1 || !strings.HasSuffix(untagged[0], "(MESSAGES "+want+")") {
			t.Fatalf("STATUS %s: %q, want MESSAGES %s", folder, untagged, want)
		}
	}
}
