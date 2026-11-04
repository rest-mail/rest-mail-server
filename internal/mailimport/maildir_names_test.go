package mailimport

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	apitypes "github.com/rest-mail/rest-mail-server/internal/api/types"
)

func maildirFolders(t *testing.T, directories ...string) ([]string, error) {
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
	err := Maildir(t.Context(), root, 1024, func(_ string, message apitypes.ImportMessageBody) error {
		folders = append(folders, message.Folder)
		return nil
	})
	slices.Sort(folders)
	return folders, err
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

// A name that is neither is refused, never stored under a name it never had.
func TestMaildirRefusesMalformedModifiedUTF7(t *testing.T) {
	for _, directory := range []string{
		".Tom & Jerry", // "&" not written as "&-"
		".R&AOk",       // run not closed
		".&AGE-",       // "a" encoded although it represents itself
		".&AOk-&AOk-",  // superfluous shift between runs
		".&2D0-",       // unpaired surrogate
		".&AO!-",       // not modified BASE64
		".Arch\xffive", // neither ASCII nor UTF-8
	} {
		if folders, err := maildirFolders(t, directory); err == nil {
			t.Errorf("%q imported as %q", directory, folders)
		}
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
		if back, err := maildirFolder(got); err != nil || back != folder {
			t.Errorf("%q read back as %q error %v", got, back, err)
		}
	}
	if _, err := MaildirDirectory("v1.2"); err == nil {
		t.Error(`a folder containing "." was given a directory`)
	}
}
