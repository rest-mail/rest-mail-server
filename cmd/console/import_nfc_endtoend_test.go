package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// nfcMaildir is a Maildir holding two messages in one Maildir++ folder, named as a decomposing
// system names it.
func nfcMaildir(t *testing.T, folder string) string {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{"", "." + folder} {
		for _, sub := range []string{"cur", "new", "tmp"} {
			if err := os.MkdirAll(filepath.Join(root, dir, sub), 0755); err != nil {
				t.Fatal(err)
			}
		}
	}
	for name, id := range map[string]string{"1700000001.M1P1.mail:2,S": "one", "1700000002.M2P2.mail:2,S": "two"} {
		raw := "From: a@example.test\r\nTo: dest@example.test\r\nSubject: " + id + "\r\nMessage-ID: <" + id + "@example.test>\r\nDate: Fri, 07 Jun 2019 08:09:10 +0000\r\n\r\nbody\r\n"
		if err := os.WriteFile(filepath.Join(root, "."+folder, "cur", name), []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// foldersHolding is how many messages each folder of the account holds, its marker among them.
func (s *migrationServer) foldersHolding(t *testing.T, account uint) map[string]int {
	t.Helper()
	counts := map[string]int{}
	for _, message := range s.storedMail(t, account).Messages {
		counts[message.Folder]++
	}
	return counts
}

// Owner's rule for #1669, through the real gateway, API, database and importer. A name that
// matches an existing folder once converted is refused on IMAP. The importer instead appends
// " (2)" and imports the mail there, keeps a whole source folder together, and adds nothing when
// it runs again. A folder with no match just takes the converted name.
func TestConvertedFolderNamesThroughEveryPath(t *testing.T) {
	const (
		nfc = "Caf\u00e9"
		nfd = "Cafe\u0301"
	)
	s := newMigrationServer(t)
	address := "dest@" + migrationDomain
	code, outcome := s.importPasswdFile(t, s.session, address+":"+dovecotSHA512Hash+"\n")
	if code != exitOK || len(outcome["created"]) != 1 {
		t.Fatalf("create the destination: exit %d, %+v", code, outcome)
	}
	account := outcome["created"][0].ID

	client, err := s.signIn(t, address, dovecotSHA512Password)
	if err != nil {
		t.Fatalf("sign in: %v", err)
	}
	// This client parses what it is sent: it has to survive the LIST that carries OLDNAME.
	if err := client.Create(nfd, nil).Wait(); err != nil {
		t.Fatalf("CREATE of the decomposed name: %v", err)
	}
	for _, spelling := range []string{nfd, nfc} {
		err := client.Create(spelling, nil).Wait()
		if err == nil || !strings.Contains(err.Error(), "already exists") {
			t.Fatalf("CREATE %q of an existing folder: %v, want it refused", spelling, err)
		}
	}
	message := "From: a@example.test\r\nSubject: appended\r\nMessage-ID: <appended@example.test>\r\n\r\nbody\r\n"
	appending := client.Append(nfd, int64(len(message)), nil)
	if _, err := appending.Write([]byte(message)); err != nil {
		t.Fatalf("APPEND: %v", err)
	}
	if err := appending.Close(); err != nil {
		t.Fatalf("APPEND: %v", err)
	}
	if _, err := appending.Wait(); err != nil {
		t.Fatalf("APPEND to the decomposed name: %v", err)
	}
	if selected, err := client.Select(nfd, nil).Wait(); err != nil || selected.NumMessages != 1 {
		t.Fatalf("SELECT of the decomposed name: %+v, %v", selected, err)
	}
	if err := client.Logout().Wait(); err != nil {
		t.Fatalf("LOGOUT: %v", err)
	}
	before := s.foldersHolding(t, account)
	if before[nfc] != 2 || before[nfd] != 0 {
		t.Fatalf("folders after CREATE and APPEND: %v, want the marker and the message under the NFC name only", before)
	}

	root := nfcMaildir(t, nfd)
	if got := s.importMaildir(t, account, root); got != (maildirSummary{Messages: 2, Imported: 2}) {
		t.Fatalf("first import: %+v", got)
	}
	after := s.foldersHolding(t, account)
	if after[nfc] != 2 || after[nfc+" (2)"] != 2 || after[nfd] != 0 {
		t.Fatalf("folders after the import: %v, want both messages in %q and nothing mixed", after, nfc+" (2)")
	}
	if got := s.importMaildir(t, account, root); got != (maildirSummary{Messages: 2, Skipped: 2}) {
		t.Fatalf("second import: %+v, want every message found again", got)
	}
	if again := s.foldersHolding(t, account); len(again) != len(after) || again[nfc+" (2)"] != 2 || again[nfc+" (3)"] != 0 {
		t.Fatalf("folders after the second import: %v", again)
	}

	// No folder has the converted name here, so it takes it, whole, on both runs.
	const nfdFree, nfcFree = "Tourne\u0301", "Tourn\u00e9"
	root = nfcMaildir(t, nfdFree)
	if got := s.importMaildir(t, account, root); got != (maildirSummary{Messages: 2, Imported: 2}) {
		t.Fatalf("free name, first import: %+v", got)
	}
	if got := s.importMaildir(t, account, root); got != (maildirSummary{Messages: 2, Skipped: 2}) {
		t.Fatalf("free name, second import: %+v", got)
	}
	final := s.foldersHolding(t, account)
	if final[nfcFree] != 2 || final[nfcFree+" (2)"] != 0 || final[nfdFree] != 0 {
		var names []string
		for name := range final {
			names = append(names, name)
		}
		sort.Strings(names)
		t.Fatalf("free name: %v (%q), want both messages in %q", final, names, nfcFree)
	}
}
