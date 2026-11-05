package mailimport

import (
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"

	apitypes "github.com/rest-mail/rest-mail-server/internal/api/types"
)

func maildirFolders(t *testing.T, directories ...string) ([]string, error) {
	t.Helper()
	folders, _, err := maildirImport(t, directories...)
	return folders, err
}

// maildirImport imports one message from each source directory and returns the
// sorted destination folders, one per message, and the renames reported.
// filesystemTakesName reports whether a directory of this name can exist here.
// APFS refuses a name that is not UTF-8, so a Mac cannot hold that source at all.
// Linux can, and CI's run there still tests it.
func filesystemTakesName(t *testing.T, name string) bool {
	t.Helper()
	err := os.Mkdir(filepath.Join(t.TempDir(), name), 0755)
	if errors.Is(err, syscall.EILSEQ) {
		t.Logf("%q: this filesystem refuses the name, so this source cannot exist here", name)
		return false
	}
	if err != nil {
		t.Fatal(err)
	}
	return true
}

func maildirImport(t *testing.T, directories ...string) ([]string, []FolderRename, error) {
	t.Helper()
	root := sourceMaildir(t)
	for _, directory := range directories {
		for _, sub := range []string{"cur", "new"} {
			if err := os.MkdirAll(filepath.Join(root, directory, sub), 0755); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(root, directory, "cur", "1:2,S"), []byte("Message-ID: <a@example.test>\r\n\r\nbody\r\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var folders []string
	var renames []FolderRename
	err := Maildir(t.Context(), root, 1024, func(_ string, message apitypes.ImportMessageBody) error {
		folders = append(folders, message.Folder)
		return nil
	}, func(rename FolderRename) { renames = append(renames, rename) })
	slices.Sort(folders)
	return folders, renames, err
}

// #1833: Dovecot names Maildir++ folders in modified UTF-7, each "." level on
// its own; the folder imported is the Unicode name its IMAP users saw.
func TestMaildirDecodesDovecotModifiedUTF7Folders(t *testing.T) {
	folders, err := maildirFolders(t, ".", ".R&AOk-sum&AOk-.&ZcWITA-", ".Archive.2020", ".Tom &- Jerry", ".Caf&AOk-")
	want := []string{"Archive/2020", "Café", "INBOX", "Résumé/旅行", "Tom & Jerry"}
	if err != nil || !slices.Equal(folders, want) {
		t.Fatalf("folders %q error %v, want %q", folders, err, want)
	}
}

// A level with octets outside US-ASCII cannot be modified UTF-7, so it is the
// UTF-8 name a Maildir written with UTF-8 names holds, kept as it is.
func TestMaildirKeepsUTF8Folders(t *testing.T) {
	folders, err := maildirFolders(t, ".Résumé.旅行")
	if err != nil || !slices.Equal(folders, []string{"Résumé/旅行"}) {
		t.Fatalf("folders %q error %v", folders, err)
	}
}

// #1833 owner decision: a name that is neither never stops the import. The
// folder is imported under its literal text, made a legal folder name, and the
// import reports it. Every message arrives.
func TestMaildirImportsMalformedNamesUnderDerivedNames(t *testing.T) {
	for directory, want := range map[string]string{
		".Tom & Jerry":          "Tom & Jerry",      // "&" not written as "&-"
		".R&AOk":                "R&AOk",            // run not closed
		".&AGE-":                "&AGE-",            // "a" encoded although it represents itself
		".&AOk-&AOk-":           "&AOk-&AOk-",       // superfluous shift between runs
		".&2D0-":                "&2D0-",            // unpaired surrogate
		".&AO!-":                "&AO!-",            // not modified BASE64
		".Arch\xffive":          "Arch\uFFFDive",    // neither ASCII nor UTF-8
		".Caf&AOk-.Tom & Jerry": "Café/Tom & Jerry", // only the bad level is literal
		".Tab\there":            "Tab\uFFFDhere",    // a control character is not a legal name
	} {
		if !filesystemTakesName(t, directory) {
			continue
		}
		folders, renames, err := maildirImport(t, directory)
		if err != nil || !slices.Equal(folders, []string{want}) {
			t.Errorf("%q: folders %q error %v, want %q", directory, folders, err, want)
			continue
		}
		if len(renames) != 1 || renames[0].Source != directory || renames[0].Folder != want || renames[0].Reason == "" {
			t.Errorf("%q: renames %+v, want one report naming %q", directory, renames, want)
		}
	}
}

// Two source folders that map to one name are both imported: the one whose
// own name maps cleanly keeps it, the other is "<name> (2)", then "(3)", and
// each rename is reported. Every message arrives.
func TestMaildirImportsCollidingFoldersUnderNumberedNames(t *testing.T) {
	// ".Tom &- Jerry" decodes to "Tom & Jerry"; ".Tom & Jerry" is malformed and
	// derives the same literal text; ".Tom & Jerry (2)", also malformed, derives
	// the next name, so it is skipped.
	folders, renames, err := maildirImport(t, ".Tom & Jerry", ".Tom &- Jerry", ".Tom & Jerry (2)", ".Caf&AOk-", ".Café")
	want := []string{"Café", "Café (2)", "Tom & Jerry", "Tom & Jerry (2)", "Tom & Jerry (3)"}
	if err != nil || !slices.Equal(folders, want) {
		t.Fatalf("folders %q error %v, want %q", folders, err, want)
	}
	reported := map[string]string{}
	for _, rename := range renames {
		reported[rename.Source] = rename.Folder
	}
	if !maps.Equal(reported, map[string]string{".Tom & Jerry": "Tom & Jerry (3)", ".Tom & Jerry (2)": "Tom & Jerry (2)", ".Café": "Café (2)"}) {
		t.Fatalf("renames %+v", renames)
	}
	// A rerun names every folder the same way, so it resumes into them.
	again, _, err := maildirImport(t, ".Café", ".Tom & Jerry (2)", ".Tom &- Jerry", ".Caf&AOk-", ".Tom & Jerry")
	if err != nil || !slices.Equal(again, want) {
		t.Fatalf("rerun folders %q error %v", again, err)
	}
}

// export-maildir writes what import-maildir and Dovecot read back.
func TestMaildirDirectoryRoundTrips(t *testing.T) {
	for folder, directory := range map[string]string{
		"INBOX":        ".",
		"Archive/2020": ".Archive.2020",
		"Résumé/旅行":    ".R&AOk-sum&AOk-.&ZcWITA-",
		"Tom & Jerry":  ".Tom &- Jerry",
		"😀":            ".&2D3eAA-",
	} {
		got, err := MaildirDirectory(folder)
		if err != nil || got != directory {
			t.Errorf("%q: directory %q error %v, want %q", folder, got, err, directory)
			continue
		}
		if folder == "INBOX" {
			continue
		}
		if back, reason := maildirFolder(got); reason != "" || back != folder {
			t.Errorf("%q read back as %q (%s)", got, back, reason)
		}
	}
	if _, err := MaildirDirectory("v1.2"); err == nil {
		t.Error(`a folder containing "." was given a directory`)
	}
}
