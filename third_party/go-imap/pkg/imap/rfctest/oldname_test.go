package rfctest

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/rest-mail/go-imap/pkg/imap"
)

// The same name in both forms, as a store keeps it and as a decomposing client
// sends it, in modified UTF-7 for a session that has not enabled UTF-8.
const (
	cafeNFC     = "Caf\u00e9"
	cafeNFD     = "Cafe\u0301"
	cafeNFCWire = "Caf&AOk-"
	cafeNFDWire = "Cafe&AwE-"
)

// nfcNames is the store's normal form for the names these tests use. This module
// has no dependency that knows Unicode normalization, which is why a store
// supplies it; the gateway's session tests run the real one.
func nfcNames(name string) string { return strings.ReplaceAll(name, "e\u0301", "\u00e9") }

// normalizingStore is a folderStore that keeps names in NFC and records the
// destination of each APPEND.
type normalizingStore struct {
	*folderStore
	appended []string
}

func (*normalizingStore) NormalizeName(name string) string { return nfcNames(name) }

func (m *normalizingStore) Append(folder string, _ imap.FlagUpdate, _ []byte) error {
	m.appended = append(m.appended, folder)
	return nil
}

func oldNameClient(t *testing.T, folders ...string) (*normalizingStore, *client) {
	t.Helper()
	all := map[string][]imap.Message{"INBOX": {{UID: 1}}}
	for _, name := range folders {
		all[name] = []imap.Message{{UID: 2}}
	}
	store := &normalizingStore{folderStore: newFolderStore(all)}
	c := dialMailbox(t, store)
	c.line()
	c.login()
	return store, c
}

// oldNameLines is the LIST responses among untagged.
func oldNameLines(untagged []string) []string {
	var lines []string
	for _, line := range untagged {
		if strings.HasPrefix(line, "* LIST ") {
			lines = append(lines, line)
		}
	}
	return lines
}

// TestOldNameSelectExamine covers RFC 9051 §6.3.2: "If the server allows
// denormalized UTF-8 mailbox names ... and the supplied mailbox name differs from
// the normalized version, the server MUST return LIST with the OLDNAME extended
// data item." The client's other spelling reaches the folder, and a name already
// in the normal form, or INBOX in another case, draws no LIST.
//
// checklist: IMAP2-CMD-116
func TestOldNameSelectExamine(t *testing.T) {
	for _, cmd := range []string{"SELECT", "EXAMINE"} {
		t.Run(cmd, func(t *testing.T) {
			_, c := oldNameClient(t, cafeNFC)
			untagged, tagged := c.command("s1", `%s %q`, cmd, cafeNFDWire)
			requireOK(t, tagged, "s1")
			requireExactly(t, oldNameLines(untagged), fmt.Sprintf(`* LIST () "/" %q ("OLDNAME" (%q))`, cafeNFCWire, cafeNFDWire))
			requireLine(t, untagged, "* 1 EXISTS")

			for _, name := range []string{cafeNFCWire, "inbox"} {
				untagged, tagged = c.command("s2", `%s %q`, cmd, name)
				requireOK(t, tagged, "s2")
				requireExactly(t, oldNameLines(untagged))
			}

			// A refused selection used no name, so it reports none.
			untagged, tagged = c.command("s3", `%s "Missing%s"`, cmd, "e&AwE-")
			if !strings.HasPrefix(tagged, "s3 NO") {
				t.Fatalf("%s of a missing name: %q", cmd, tagged)
			}
			requireExactly(t, oldNameLines(untagged))
		})
	}
}

// TestOldNameCreate covers RFC 9051 §6.3.4: the folder is created under the
// normal form, "an untagged LIST with an OLDNAME extended data item" says so, and
// the tag is the registered "OLDNAME" as §9 formats it. A name that matches an
// extant folder once converted is refused like any other duplicate, and reports
// nothing because nothing was created.
//
// checklist: IMAP2-CORE-31 IMAP2-CMD-79 IMAP2-CMD-116 IMAP2-SEC-228
func TestOldNameCreate(t *testing.T) {
	store, c := oldNameClient(t)
	untagged, tagged := c.command("c1", `CREATE %q`, cafeNFDWire)
	if tagged != "c1 OK CREATE completed" {
		t.Fatalf("CREATE: tagged %q", tagged)
	}
	requireExactly(t, untagged, fmt.Sprintf(`* LIST () "/" %q ("OLDNAME" (%q))`, cafeNFCWire, cafeNFDWire))
	if got := store.names(); got != cafeNFC+" INBOX" {
		t.Fatalf("folders after CREATE: %q, want the normal form only", got)
	}

	for _, spelling := range []string{cafeNFDWire, cafeNFCWire} {
		untagged, tagged = c.command("c2", `CREATE %q`, spelling)
		if tagged != "c2 NO [ALREADYEXISTS] Mailbox already exists" {
			t.Fatalf("CREATE %s of an extant name: tagged %q", spelling, tagged)
		}
		requireExactly(t, untagged)
	}

	untagged, tagged = c.command("c3", `CREATE "Plain"`)
	requireOK(t, tagged, "c3")
	requireExactly(t, untagged)
}

// TestOldNameDelete covers RFC 9051 §6.3.5: "an untagged LIST with the
// "\NonExistent" attribute and OLDNAME extended data item".
//
// checklist: IMAP2-CMD-87 IMAP2-CMD-116
func TestOldNameDelete(t *testing.T) {
	store, c := oldNameClient(t, cafeNFC, "Plain")
	untagged, tagged := c.command("d1", `DELETE %q`, cafeNFDWire)
	if tagged != "d1 OK DELETE completed" {
		t.Fatalf("DELETE: tagged %q", tagged)
	}
	requireExactly(t, untagged, fmt.Sprintf(`* LIST (\NonExistent) "/" %q ("OLDNAME" (%q))`, cafeNFCWire, cafeNFDWire))
	if got := store.names(); got != "INBOX Plain" {
		t.Fatalf("folders after DELETE: %q", got)
	}

	untagged, tagged = c.command("d2", `DELETE %q`, cafeNFDWire)
	if !strings.HasPrefix(tagged, "d2 NO [NONEXISTENT]") {
		t.Fatalf("DELETE of a missing name: tagged %q", tagged)
	}
	requireExactly(t, untagged)

	untagged, tagged = c.command("d3", `DELETE "Plain"`)
	requireOK(t, tagged, "d3")
	requireExactly(t, untagged)
}

// TestOldNameRename covers RFC 9051 §6.3.6: when either name changed, the LIST
// has "the OLDNAME value being the supplied existing mailbox name and the name
// parameter being the normalized new mailbox name". A destination that matches an
// extant folder once converted is refused.
//
// checklist: IMAP2-CMD-91 IMAP2-CMD-116
func TestOldNameRename(t *testing.T) {
	for _, tc := range []struct {
		name, folder, from, to, want string
	}{
		{"source", cafeNFC, cafeNFDWire, "Plain", fmt.Sprintf(`* LIST () "/" "Plain" ("OLDNAME" (%q))`, cafeNFDWire)},
		{"destination", "Plain", "Plain", cafeNFDWire, fmt.Sprintf(`* LIST () "/" %q ("OLDNAME" ("Plain"))`, cafeNFCWire)},
		{"both", "Tourne\u0301", "Tourne&AwE-", cafeNFDWire, fmt.Sprintf(`* LIST () "/" %q ("OLDNAME" ("Tourne&AwE-"))`, cafeNFCWire)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, c := oldNameClient(t, nfcNames(tc.folder))
			untagged, tagged := c.command("r1", `RENAME %q %q`, tc.from, tc.to)
			if tagged != "r1 OK RENAME completed" {
				t.Fatalf("RENAME: tagged %q", tagged)
			}
			requireExactly(t, untagged, tc.want)
			if !slices.Contains(strings.Fields(store.names()), nfcNames(strings.ReplaceAll(strings.ReplaceAll(tc.to, cafeNFDWire, cafeNFD), cafeNFCWire, cafeNFC))) {
				t.Fatalf("folders after RENAME: %q", store.names())
			}
		})
	}

	store, c := oldNameClient(t, cafeNFC, "Plain")
	untagged, tagged := c.command("r2", `RENAME "Plain" %q`, cafeNFDWire)
	if tagged != "r2 NO [ALREADYEXISTS] Mailbox already exists" {
		t.Fatalf("RENAME onto an extant name: tagged %q", tagged)
	}
	requireExactly(t, untagged)
	if got := store.names(); got != cafeNFC+" INBOX Plain" {
		t.Fatalf("folders after the refused RENAME: %q", got)
	}

	untagged, tagged = c.command("r3", `RENAME "Plain" "Other"`)
	requireOK(t, tagged, "r3")
	requireExactly(t, untagged)
}

// appendTo sends APPEND of a small message to folder, as written on the wire.
func appendTo(t *testing.T, c *client, tag, folder string) (untagged []string, tagged string) {
	t.Helper()
	message := "Subject: x\r\n\r\nx\r\n"
	if _, err := fmt.Fprintf(c.conn, "%s APPEND %q {%d+}\r\n%s\r\n", tag, folder, len(message), message); err != nil {
		t.Fatal(err)
	}
	for {
		line := c.line()
		if strings.HasPrefix(line, tag+" ") {
			return untagged, line
		}
		untagged = append(untagged, line)
	}
}

// TestOldNameAppend covers RFC 9051 §6.3.12: the message is stored under the
// normal form and "an untagged LIST with an OLDNAME extended data item" says so.
//
// checklist: IMAP2-CMD-132
func TestOldNameAppend(t *testing.T) {
	store, c := oldNameClient(t, cafeNFC)
	untagged, tagged := appendTo(t, c, "a1", cafeNFDWire)
	requireOK(t, tagged, "a1")
	requireExactly(t, untagged, fmt.Sprintf(`* LIST () "/" %q ("OLDNAME" (%q))`, cafeNFCWire, cafeNFDWire))

	untagged, tagged = appendTo(t, c, "a2", cafeNFCWire)
	requireOK(t, tagged, "a2")
	requireExactly(t, untagged)
	if want := []string{cafeNFC, cafeNFC}; !slices.Equal(store.appended, want) {
		t.Fatalf("appended to %q, want %q", store.appended, want)
	}
}

// TestOldNameKeepsTheFolderRole: "the list of mailbox attributes MUST be
// accurate" (RFC 9051 §6.3.2), so a folder with a role keeps it on this LIST.
//
// checklist: IMAP2-CMD-116
func TestOldNameKeepsTheFolderRole(t *testing.T) {
	store := &roleNormalizingStore{normalizingStore: normalizingStore{folderStore: newFolderStore(map[string][]imap.Message{"INBOX": {{UID: 1}}, cafeNFC: {{UID: 2}}})}}
	c := dialMailbox(t, store)
	c.line()
	c.login()
	untagged, tagged := c.command("s1", `SELECT %q`, cafeNFDWire)
	requireOK(t, tagged, "s1")
	requireExactly(t, oldNameLines(untagged), fmt.Sprintf(`* LIST (\Archive) "/" %q ("OLDNAME" (%q))`, cafeNFCWire, cafeNFDWire))
}

type roleNormalizingStore struct{ normalizingStore }

func (m *roleNormalizingStore) Folders() ([]imap.Folder, error) {
	folders, err := m.normalizingStore.Folders()
	for i := range folders {
		if folders[i].Name == cafeNFC {
			folders[i].SpecialUse = imap.SpecialUseArchive
		}
	}
	return folders, err
}

// TestNormalizedNameReachesTheFolder covers RFC 9051 §5.1's "consistently
// resolving": every other command that carries a name reaches the same folder from
// the client's other spelling, a LIST pattern included, and answers with the
// store's. Only the commands RFC 9051 §6.3.9.7 names report OLDNAME.
//
// checklist: IMAP2-CORE-31
func TestNormalizedNameReachesTheFolder(t *testing.T) {
	store, c := oldNameClient(t, cafeNFC)

	untagged, tagged := c.command("t1", `STATUS %q (MESSAGES)`, cafeNFDWire)
	requireOK(t, tagged, "t1")
	requireExactly(t, untagged, fmt.Sprintf(`* STATUS %q (MESSAGES 1)`, cafeNFCWire))

	for _, pattern := range []string{cafeNFDWire, "Cafe&AwE-*", "Caf*"} {
		untagged, tagged = c.command("t2", `LIST "" %q`, pattern)
		requireOK(t, tagged, "t2")
		requireExactly(t, untagged, fmt.Sprintf(`* LIST () "/" %q`, cafeNFCWire))
	}

	if _, tagged = c.command("t3", `SUBSCRIBE %q`, cafeNFDWire); !strings.HasPrefix(tagged, "t3 OK") {
		t.Fatalf("SUBSCRIBE: %q", tagged)
	}
	if got := store.names(); got != cafeNFC+" INBOX" {
		t.Fatalf("folders: %q", got)
	}
}

// TestOldNameImplicitFolders: a store that normalizes but keeps implicit folders
// reports OLDNAME from the same commands.
//
// checklist: IMAP2-CMD-116
func TestOldNameImplicitFolders(t *testing.T) {
	box := &normalizingNames{namesMailbox: newNamesMailbox(cafeNFC)}
	c := selectionClient(t, box)
	want := fmt.Sprintf(`* LIST () "/" %q ("OLDNAME" (%q))`, cafeNFCWire, cafeNFDWire)
	untagged, tagged := c.command("c1", `CREATE %q`, cafeNFDWire)
	requireOK(t, tagged, "c1")
	requireExactly(t, untagged, want)

	untagged, tagged = c.command("r1", `RENAME "Plain" %q`, cafeNFDWire)
	requireOK(t, tagged, "r1")
	requireExactly(t, untagged, fmt.Sprintf(`* LIST () "/" %q ("OLDNAME" ("Plain"))`, cafeNFCWire))

	untagged, tagged = c.command("d1", `DELETE %q`, cafeNFDWire)
	requireOK(t, tagged, "d1")
	requireExactly(t, untagged, fmt.Sprintf(`* LIST (\NonExistent) "/" %q ("OLDNAME" (%q))`, cafeNFCWire, cafeNFDWire))
}

type normalizingNames struct{ *namesMailbox }

func (*normalizingNames) NormalizeName(name string) string { return nfcNames(name) }

// utf8NormalizingStore also takes UTF8=ACCEPT.
type utf8NormalizingStore struct{ normalizingStore }

func (*utf8NormalizingStore) EnableUTF8() error { return nil }

// TestOldNameInUTF8: once a client has enabled UTF-8 both names are written in
// UTF-8, each as a literal, since a quoted string here holds no 8-bit octet.
//
// checklist: IMAP2-CMD-116
func TestOldNameInUTF8(t *testing.T) {
	store := &utf8NormalizingStore{normalizingStore{folderStore: newFolderStore(map[string][]imap.Message{"INBOX": {{UID: 1}}})}}
	c := dialMailbox(t, store)
	c.line()
	c.login()
	_, tagged := c.command("e1", "ENABLE UTF8=ACCEPT")
	requireOK(t, tagged, "e1")
	if _, err := fmt.Fprintf(c.conn, "c1 CREATE \"%s\"\r\n", cafeNFD); err != nil {
		t.Fatal(err)
	}
	var lines []string
	for {
		line := c.line()
		lines = append(lines, line)
		if strings.HasPrefix(line, "c1 ") {
			break
		}
	}
	want := []string{
		fmt.Sprintf(`* LIST () "/" {%d}`, len(cafeNFC)),
		fmt.Sprintf(`%s ("OLDNAME" ({%d}`, cafeNFC, len(cafeNFD)),
		cafeNFD + "))",
		"c1 OK CREATE completed",
	}
	if !slices.Equal(lines, want) {
		t.Fatalf("CREATE: %q, want %q", lines, want)
	}
	if got := store.names(); got != cafeNFC+" INBOX" {
		t.Fatalf("folders: %q", got)
	}
}

// TestNoNormalizerKeepsTheSpelling: a Mailbox without imap.NameNormalizer keeps
// each name as the client spelled it, and nothing reports OLDNAME.
//
// checklist: IMAP2-CORE-31
func TestNoNormalizerKeepsTheSpelling(t *testing.T) {
	store := newFolderStore(map[string][]imap.Message{"INBOX": {{UID: 1}}, cafeNFC: {{UID: 2}}})
	c := dialMailbox(t, store)
	c.line()
	c.login()
	untagged, tagged := c.command("c1", `CREATE %q`, cafeNFDWire)
	requireOK(t, tagged, "c1")
	requireExactly(t, untagged)
	if !slices.Contains(strings.Fields(store.names()), cafeNFD) {
		t.Fatalf("folders: %q, want the client's spelling", store.names())
	}
	untagged, tagged = c.command("l1", `LIST "" %q`, cafeNFDWire)
	requireOK(t, tagged, "l1")
	requireExactly(t, untagged, fmt.Sprintf(`* LIST () "/" %q`, cafeNFDWire))
}
