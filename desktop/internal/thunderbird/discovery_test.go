package thunderbird

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixture(t *testing.T) (string, []Profile) {
	t.Helper()
	root := t.TempDir()
	profile := filepath.Join(root, "Profiles", "sample")
	files := map[string]string{
		"profiles.ini": "[General]\nStartWithLastProfile=1\n[Profile0]\nName=Synthetic\nIsRelative=1\nPath=Profiles/sample\n",
		"Profiles/sample/Mail/Local Folders/Inbox":             sampleMessage("0001"),
		"Profiles/sample/Mail/Local Folders/Inbox.msf":         "not mail",
		"Profiles/sample/Mail/Local Folders/Archives.sbd/2024": sampleMessage("0000") + sampleMessage("0008"),
		"Profiles/sample/ImapMail/imap.example.test/Sent":      strings.ReplaceAll(sampleMessage("0004"), "\n", "\r\n"),
	}
	for name, data := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(profile, "Mail", "Local Folders", "Inbox"), filepath.Join(profile, "Mail", "Local Folders", "Symlink")); err != nil {
		t.Fatal(err)
	}
	profiles, err := discoverRoots([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	return root, profiles
}

func sampleMessage(status string) string {
	return "From - Tue Jan  2 03:04:05 2024\nMessage-ID: <synthetic@example.test>\nDate: Tue, 02 Jan 2024 03:04:05 +0000\nX-Mozilla-Status: " + status + "\n\nbody\n\n"
}

func TestProfilesNestedFoldersCountsAndIndexes(t *testing.T) {
	_, profiles := fixture(t)
	if len(profiles) != 1 || len(profiles[0].Folders) != 3 {
		t.Fatalf("profiles=%d folders=%d", len(profiles), len(profiles[0].Folders))
	}
	found := map[string]string{}
	for _, folder := range profiles[0].Folders {
		found[folder.Name] = folder.Destination
		file, err := os.Open(filepath.Join(profiles[0].path, filepath.FromSlash(folder.ID)))
		if err != nil {
			t.Fatal(err)
		}
		messages, expunged, err := countMessages(context.Background(), file)
		file.Close()
		if err != nil || messages != 1 || folder.Bytes == 0 {
			t.Fatalf("count=%d error=%v", messages, err)
		}
		if folder.Name == "Archives/2024" && expunged != 1 {
			t.Fatal("expunged not excluded")
		}
	}
	if found["Inbox"] != "INBOX" || found["Archives/2024"] != "Archive/2024" || found["Sent"] != "Sent" {
		t.Fatalf("mapping=%v", found)
	}
}

func TestPlatformRootsAndSpecialFolders(t *testing.T) {
	if accountLabel("Mail", "private@example.test", 2) != "Mail account 2 · example.test" || accountLabel("ImapMail", "imap.example.test", 3) != "IMAP cache 3 · imap.example.test" {
		t.Fatal("account host labels are missing or expose addresses")
	}
	for _, platform := range []string{"darwin", "windows", "linux"} {
		if len(profileRoots("/home/test", platform, "/appdata")) == 0 {
			t.Fatal(platform)
		}
	}
	for name, want := range map[string]string{"INBOX": "INBOX", "Sent Items": "Sent", "Drafts": "Drafts", "Deleted Items": "Trash", "Archives/2023/Work": "Archive/2023/Work", "Custom/Nested": "Custom/Nested"} {
		if got := destination(name); got != want {
			t.Fatalf("%s: %s", name, got)
		}
	}
}

func TestFolderNamesWithExtensionsDoNotHideMailOrIncludeOtherFiles(t *testing.T) {
	root, profiles := fixture(t)
	account := filepath.Join(profiles[0].path, "Mail", "Local Folders")
	for name, data := range map[string]string{"Archive.json": sampleMessage("0000"), ".Hidden": sampleMessage("0000"), "filterlog.html": "<html>not mail</html>"} {
		if err := os.WriteFile(filepath.Join(account, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	profiles, err := discoverRoots([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, folder := range profiles[0].Folders {
		found[folder.Name] = true
	}
	if !found["Archive.json"] || !found[".Hidden"] || found["filterlog.html"] {
		t.Fatal("folder content and index detection failed")
	}
}

func TestCountStreamsLongLinesAndCancellation(t *testing.T) {
	source := sampleMessage("0000") + strings.Repeat("x", 2<<20) + "\n"
	messages, _, err := countMessages(context.Background(), strings.NewReader(source))
	if err != nil || messages != 1 {
		t.Fatalf("count=%d error=%v", messages, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := countMessages(ctx, strings.NewReader(source)); err == nil {
		t.Fatal("cancel ignored")
	}
}

func TestOpenFolderRejectsTraversalAndSymlinks(t *testing.T) {
	_, profiles := fixture(t)
	root, err := os.OpenRoot(profiles[0].path)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, id := range []string{"../profiles.ini", "Mail/Local Folders/Symlink"} {
		if file, err := openFolder(root, id); err == nil {
			file.Close()
			t.Fatal("unsafe source accepted")
		}
	}
}
