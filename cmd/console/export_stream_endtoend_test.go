package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
)

// #2116 AC03: an export is complete, streamed and scoped. These run the
// restmail export commands against the real public router on PostgreSQL.

// seedMaildir writes a Maildir for import-maildir: folder "" is INBOX, any
// other a Maildir++ folder, each message seen and dated date.
func seedMaildir(t *testing.T, folders map[string][]string, date time.Time) string {
	t.Helper()
	root := t.TempDir()
	if _, ok := folders[""]; !ok {
		folders[""] = nil // import-maildir reads INBOX's cur and new
	}
	n := 0
	for folder, messages := range folders {
		dir := root
		if folder != "" {
			dir = filepath.Join(root, "."+folder)
		}
		for _, sub := range []string{"cur", "new", "tmp"} {
			if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
				t.Fatal(err)
			}
		}
		if folder != "" {
			if err := os.WriteFile(filepath.Join(dir, "maildirfolder"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		for _, raw := range messages {
			n++
			path := filepath.Join(dir, "cur", fmt.Sprintf("17000%05d.M%dP1.seed:2,S", n, n))
			if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(path, date, date); err != nil {
				t.Fatal(err)
			}
		}
	}
	return root
}

// exportChildEnv makes the test binary an export command instead: the
// exporter runs in a process of its own, as restmail does, so its memory is
// measured apart from the server the test runs in-process.
const exportChildEnv = "RESTMAIL_TEST_EXPORT_CHILD"

// exportChild runs export-maildir with the arguments in exportChildEnv and
// exits, if the variable is set. With mode "hold" it then also reads every
// exported file into memory and keeps it: the control a bound must catch.
func exportChild() bool {
	encoded := os.Getenv(exportChildEnv)
	if encoded == "" {
		return false
	}
	var child struct {
		Mode string
		Args []string
	}
	if err := json.Unmarshal([]byte(encoded), &child); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	code := runExportMaildir(context.Background(), child.Args, os.Getenv("RESTMAIL_API_KEY"), http.DefaultClient, os.Stdout, os.Stderr)
	if code == exitOK && child.Mode == "hold" {
		path := child.Args[slices.Index(child.Args, "--path")+1]
		var held [][]byte
		files, _ := os.ReadDir(filepath.Join(path, "cur"))
		for _, file := range files {
			raw, err := os.ReadFile(filepath.Join(path, "cur", file.Name()))
			if err != nil {
				os.Exit(1)
			}
			held = append(held, raw)
		}
		runtime.KeepAlive(held)
	}
	os.Exit(code)
	return true
}

// runExportChild runs export-maildir in a child process and answers its exit
// status, its stdout and stderr, and its peak resident memory in bytes.
func runExportChild(t *testing.T, mode, key string, args ...string) (int, []byte, string, int64) {
	t.Helper()
	encoded, err := json.Marshal(map[string]any{"Mode": mode, "Args": args})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), exportChildEnv+"="+string(encoded), "RESTMAIL_API_KEY="+key)
	var out, errs bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errs
	err = cmd.Run()
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		t.Fatal(err)
	}
	usage, ok := cmd.ProcessState.SysUsage().(*syscall.Rusage)
	if !ok {
		t.Fatalf("no resource usage for the child on %s", runtime.GOOS)
	}
	peak := int64(usage.Maxrss)
	if runtime.GOOS != "darwin" {
		peak *= 1024 // Linux reports kilobytes, macOS bytes
	}
	return cmd.ProcessState.ExitCode(), out.Bytes(), errs.String(), peak
}

// largeMessage is a message of about size bytes, distinct by i.
func largeMessage(i, size int) string {
	line := fmt.Sprintf("message %06d carries a long body, line after line.\r\n", i)
	return fmt.Sprintf("Message-ID: <large-%d@old.example>\r\nFrom: a@old.example\r\nTo: large@%s\r\nSubject: large %d\r\n\r\n%s",
		i, migrationDomain, i, strings.Repeat(line, size/len(line)+1))
}

// A folder of more than two listing pages, whose sources together are several
// times the memory the exporter may grow by: the export completes, byte for
// byte, while the exporter's peak resident memory stays within the bound of
// what exporting an empty account takes, because each listing page is read
// one element at a time and each source is copied to its file, never held
// whole. The exporter runs as a process of its own, as restmail does, so the
// in-process server's pages are not counted. The control is the same export
// followed by holding the exported files in memory: it must go over the
// bound, or the bound would not catch an export that buffers.
func TestExportMaildirStreamsAFolderPastThePageLimitInBoundedMemory(t *testing.T) {
	const (
		count = 205       // the listing's pages hold 100
		size  = 512 << 10 // per message
		bound = 24 << 20  // the exporter's growth over exporting an empty account
	)
	s := newMigrationServer(t)
	id := s.newAccount(t, "large@"+migrationDomain)
	empty := s.newAccount(t, "empty@"+migrationDomain)
	messages := make([]string, count)
	for i := range count {
		messages[i] = largeMessage(i, size)
	}
	root := seedMaildir(t, map[string][]string{"": messages}, time.Date(2020, 3, 4, 5, 6, 7, 0, time.UTC))
	if got := s.importMaildir(t, id, root); got.Imported != count {
		t.Fatalf("seed the account: %+v", got)
	}
	var stored int64
	if err := s.db.Raw("SELECT COALESCE(SUM(octet_length(raw_message)), 0) FROM messages WHERE mailbox_id = ?", id).Scan(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored < 4*bound {
		t.Fatalf("the account holds %d bytes of source, too little to tell streaming from buffering under a %d byte bound", stored, bound)
	}
	key := s.exportKey(t)
	args := func(path string) []string {
		return []string{"--server", s.apiURL, "--account-id", strconv.FormatUint(uint64(id), 10), "--path", path, "--json"}
	}

	baseArgs := args(filepath.Join(t.TempDir(), "empty"))
	baseArgs[3] = strconv.FormatUint(uint64(empty), 10)
	code, _, errs, base := runExportChild(t, "stream", key, baseArgs...)
	if code != exitOK {
		t.Fatalf("export of an empty account: exit %d: %s", code, errs)
	}

	export := filepath.Join(t.TempDir(), "export")
	code, out, errs, peak := runExportChild(t, "stream", key, args(export)...)
	if code != exitOK {
		t.Fatalf("export-maildir: exit %d: %s", code, errs)
	}
	var summary struct {
		Messages int
		Bytes    int64
	}
	if err := json.Unmarshal(out, &summary); err != nil || summary.Messages != count || summary.Bytes != stored {
		t.Fatalf("summary %s (%v), want %d messages and %d bytes", out, err, count, stored)
	}
	t.Logf("exported %d bytes with a peak resident memory of %d bytes, %d over the empty account's %d", stored, peak, peak-base, base)
	if peak-base > bound {
		t.Fatalf("exporting %d bytes grew the exporter by %d bytes, over the %d byte bound: the export holds what it writes", stored, peak-base, bound)
	}

	files, err := os.ReadDir(filepath.Join(export, "cur"))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, file := range files {
		raw, err := os.ReadFile(filepath.Join(export, "cur", file.Name()))
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, string(raw))
	}
	var want []string
	for _, message := range s.keptMail(t, id) {
		want = append(want, message.Raw)
	}
	slices.Sort(got)
	slices.Sort(want)
	if len(got) != count || !slices.Equal(got, want) {
		t.Fatalf("exported %d messages, the account holds %d, and they differ", len(got), len(want))
	}

	code, _, errs, held := runExportChild(t, "hold", key, args(filepath.Join(t.TempDir(), "held"))...)
	if code != exitOK {
		t.Fatalf("control export: exit %d: %s", code, errs)
	}
	t.Logf("holding the export took a peak resident memory of %d bytes, %d over the empty account's", held, held-base)
	if held-base <= bound {
		t.Fatalf("holding the %d exported bytes grew the exporter by only %d bytes: the bound would not catch a buffering export", stored, held-base)
	}
}

// calendarMessage is an invitation: a text body of bodyBytes, which the folder
// listing carries, and a text/calendar part with a UID of its own.
func calendarMessage(uid string, bodyBytes int) (raw, ics string) {
	ics = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Old server//EN\r\nMETHOD:REQUEST\r\nBEGIN:VEVENT\r\nUID:" + uid +
		"\r\nDTSTAMP:20261104T120000Z\r\nDTSTART:20261105T090000Z\r\nSUMMARY:" + uid + "\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	body := strings.Repeat("All the details of the meeting, at length.\r\n", bodyBytes/44+1)
	raw = "Message-ID: <" + uid + ">\r\nFrom: organizer@example.test\r\nTo: invitee@example.test\r\nSubject: " + uid +
		"\r\nDate: Wed, 4 Nov 2026 12:00:00 +0000\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=b\r\n\r\n" +
		"--b\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n" + body +
		"--b\r\nContent-Type: text/calendar; charset=utf-8; method=REQUEST\r\nContent-Transfer-Encoding: base64\r\n\r\n" +
		base64.StdEncoding.EncodeToString([]byte(ics)) + "\r\n--b--\r\n"
	return raw, ics
}

// lowerListPage makes a folder listing page over limit bytes ask again in
// smaller pages, as one over 64 MiB does in use.
func lowerListPage(t *testing.T, limit int64) {
	t.Helper()
	old := exportListPageBytes
	exportListPageBytes = limit
	t.Cleanup(func() { exportListPageBytes = old })
}

func exportCalendarWith(t *testing.T, s *migrationServer, account uint, token, path string, extra ...string) (int, map[string]any, string) {
	t.Helper()
	var out, errs bytes.Buffer
	args := append([]string{"--server", s.apiURL, "--account-id", strconv.FormatUint(uint64(account), 10), "--path", path, "--json"}, extra...)
	code := runExportData(context.Background(), "export-calendar", args, token, s.client, &out, &errs)
	var summary map[string]any
	if code == exitOK {
		if err := json.Unmarshal(out.Bytes(), &summary); err != nil {
			t.Fatalf("summary %s: %v", &out, err)
		}
	} else if out.Len() != 0 {
		t.Fatalf("a failed export printed a summary: %s", &out)
	}
	return code, summary, errs.String()
}

// exportFailure is the message of a --json export's error.
func exportFailure(t *testing.T, stderr string) string {
	t.Helper()
	var failure struct {
		Error struct{ Code, Message string }
	}
	if err := json.Unmarshal([]byte(stderr), &failure); err != nil || failure.Error.Code != "export_failed" {
		t.Fatalf("not an export_failed error: %q (%v)", stderr, err)
	}
	return failure.Error.Message
}

// The listing carries each message's text body, so a page of invitations with
// long bodies is over the page limit; the export asks again with smaller pages
// and exports every invitation, rather than refusing the folder.
func TestExportCalendarReadsAFolderWhosePagesPassTheListingLimit(t *testing.T) {
	const (
		count     = 10
		bodyBytes = 200 << 10
		pageLimit = 1 << 20
	)
	lowerListPage(t, pageLimit)
	s := newMigrationServer(t)
	id := s.newAccount(t, "invited@"+migrationDomain)
	var messages []string
	want := map[string]bool{}
	for i := range count {
		raw, ics := calendarMessage(fmt.Sprintf("long-%d@example.test", i), bodyBytes)
		messages = append(messages, raw)
		want[ics] = true
	}
	if got := s.importMaildir(t, id, seedMaildir(t, map[string][]string{"": messages}, time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC))); got.Imported != count {
		t.Fatalf("seed: %+v", got)
	}
	key := s.exportKey(t)

	// The fixture really is over the limit at the export's first page size.
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/api/v1/accounts/%d/folders/INBOX/messages?include_imap_deleted=true&include_snoozed=true&limit=100", s.apiURL, id), nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := s.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	page, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusOK || int64(len(page)) <= pageLimit {
		t.Fatalf("a full page is %d bytes (HTTP %d, %v), not over the %d byte limit", len(page), resp.StatusCode, err, pageLimit)
	}

	path := filepath.Join(t.TempDir(), "calendar")
	code, summary, errs := exportCalendarWith(t, s, id, key, path)
	if code != exitOK {
		t.Fatalf("export-calendar: exit %d: %s", code, errs)
	}
	if summary["calendars"] != float64(count) {
		t.Fatalf("summary %v, want %d calendars", summary, count)
	}
	files, err := filepath.Glob(filepath.Join(path, "*.ics"))
	if err != nil || len(files) != count {
		t.Fatalf("exported %v (%v), want %d files", files, err, count)
	}
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil || !want[string(raw)] {
			t.Fatalf("%s is not one of the invitations sent: %q (%v)", file, raw, err)
		}
		delete(want, string(raw))
	}
}

// A folder whose single message's listing is over the page limit cannot be
// exported. The export names that folder and the narrower request that would
// succeed, publishes nothing and claims nothing; the narrower request then
// completes and says what it left out. A --skip-folder naming no folder is
// refused rather than exporting what it meant to leave out.
func TestExportCalendarNamesTheFolderItCannotFinish(t *testing.T) {
	lowerListPage(t, 256<<10)
	s := newMigrationServer(t)
	id := s.newAccount(t, "huge@"+migrationDomain)
	small1, ics1 := calendarMessage("small-1@example.test", 100)
	small2, ics2 := calendarMessage("small-2@example.test", 100)
	huge, _ := calendarMessage("huge@example.test", 400<<10)
	seed := seedMaildir(t, map[string][]string{"": {small1, small2}, "Huge": {huge}}, time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC))
	if got := s.importMaildir(t, id, seed); got.Imported != 3 {
		t.Fatalf("seed: %+v", got)
	}
	key := s.exportKey(t)

	path := filepath.Join(t.TempDir(), "calendar")
	code, _, errs := exportCalendarWith(t, s, id, key, path)
	if code != exitFailed {
		t.Fatalf("export of a folder over the page limit: exit %d: %s", code, errs)
	}
	message := exportFailure(t, errs)
	if !strings.Contains(message, `folder "Huge" could not be exported`) || !strings.Contains(message, `--skip-folder "Huge"`) ||
		!strings.Contains(message, "even with one message to the page") {
		t.Fatalf("the failure does not name the folder and the narrower request: %s", message)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("a failed export published %s (%v)", path, err)
	}

	code, summary, errs := exportCalendarWith(t, s, id, key, path, "--skip-folder", "Huge")
	if code != exitOK {
		t.Fatalf("the narrower export: exit %d: %s", code, errs)
	}
	if summary["calendars"] != float64(2) || fmt.Sprint(summary["skipped_folders"]) != "[Huge]" {
		t.Fatalf("summary %v, want 2 calendars and Huge left out", summary)
	}
	files, _ := filepath.Glob(filepath.Join(path, "*.ics"))
	var got []string
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, string(raw))
	}
	slices.Sort(got)
	if want := []string{ics1, ics2}; !slices.Equal(got, want) {
		t.Fatalf("the narrower export wrote %q", got)
	}

	other := filepath.Join(t.TempDir(), "calendar")
	code, _, errs = exportCalendarWith(t, s, id, key, other, "--skip-folder", "Hug")
	if code != exitFailed || !strings.Contains(exportFailure(t, errs), `--skip-folder "Hug" names no folder`) {
		t.Fatalf("a --skip-folder naming no folder: exit %d: %s", code, errs)
	}
	if _, err := os.Lstat(other); !os.IsNotExist(err) {
		t.Fatalf("a refused export published %s (%v)", other, err)
	}
}

// The same for export-maildir: a folder holding a message over the limit asked
// for stops the export with that folder's name and the request that would
// succeed, which then exports everything else.
func TestExportMaildirNamesTheFolderItCannotFinish(t *testing.T) {
	s := newMigrationServer(t)
	id := s.newAccount(t, "archive@"+migrationDomain)
	small := func(n int) string {
		return fmt.Sprintf("Message-ID: <small-%d@old.example>\r\nFrom: a@old.example\r\nSubject: %d\r\n\r\nbody %d\r\n", n, n, n)
	}
	big := "Message-ID: <big@old.example>\r\nFrom: a@old.example\r\nSubject: big\r\n\r\n" + strings.Repeat("0123456789abcdef\r\n", 512)
	seed := seedMaildir(t, map[string][]string{"": {small(1), small(2)}, "Archive": {big, small(3)}}, time.Date(2020, 3, 4, 5, 6, 7, 0, time.UTC))
	if got := s.importMaildir(t, id, seed); got.Imported != 4 {
		t.Fatalf("seed: %+v", got)
	}
	key := s.exportKey(t)
	run := func(path string, extra ...string) (int, string, string) {
		var out, errs bytes.Buffer
		args := append([]string{"--server", s.apiURL, "--account-id", strconv.FormatUint(uint64(id), 10), "--path", path, "--json", "--max-message-bytes", "4096"}, extra...)
		code := runExportMaildir(context.Background(), args, key, s.client, &out, &errs)
		return code, out.String(), errs.String()
	}

	code, out, errs := run(filepath.Join(t.TempDir(), "all"))
	if code != exitFailed || out != "" {
		t.Fatalf("export with a message over the limit: exit %d, summary %q: %s", code, out, errs)
	}
	message := exportFailure(t, errs)
	if !strings.Contains(message, `folder "Archive" could not be exported`) || !strings.Contains(message, `--skip-folder "Archive"`) {
		t.Fatalf("the failure does not name the folder and the narrower request: %s", message)
	}

	narrower := filepath.Join(t.TempDir(), "narrower")
	code, out, errs = run(narrower, "--skip-folder", "Archive")
	if code != exitOK {
		t.Fatalf("the narrower export: exit %d: %s", code, errs)
	}
	var summary struct {
		Folders        int
		Messages       int
		SkippedFolders []string `json:"skipped_folders"`
	}
	if err := json.Unmarshal([]byte(out), &summary); err != nil || summary.Messages != 2 || !slices.Equal(summary.SkippedFolders, []string{"Archive"}) {
		t.Fatalf("summary %s (%v), want 2 messages with Archive left out", out, err)
	}
	if _, err := os.Stat(filepath.Join(narrower, ".Archive")); !os.IsNotExist(err) {
		t.Fatalf("the narrower export wrote the folder it left out (%v)", err)
	}
	files, err := os.ReadDir(filepath.Join(narrower, "cur"))
	if err != nil || len(files) != 2 {
		t.Fatalf("INBOX holds %d files (%v), want 2", len(files), err)
	}
}

// An export of an account the key may not read writes nothing, prints no
// summary, so claims no completion, and says nothing of what the account holds.
func TestExportsOfAnotherAccountLeakNothingAndClaimNothing(t *testing.T) {
	s := newMigrationServer(t)
	target := "secret-holder@" + migrationDomain
	id := s.newAccount(t, target)
	raw, _ := calendarMessage("secret-meeting@example.test", 100)
	if got := s.importMaildir(t, id, seedMaildir(t, map[string][]string{"": {raw}}, time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC))); got.Imported != 1 {
		t.Fatalf("seed: %+v", got)
	}
	requestDataExportAPI(t, s, http.MethodPost, fmt.Sprintf("/api/v1/accounts/%d/contacts/import", id), s.session,
		map[string]any{"contacts": []map[string]string{{"email": "secret-contact@example.test", "name": "Secret Contact"}}}, http.StatusOK)
	outsider := "outsider@" + migrationDomain
	outsiderID := s.newAccount(t, outsider)
	key := dataExportKey(t, s, outsiderID, outsider, models.CapMailRead, models.CapMailExport)

	for _, command := range []string{"export-maildir", "export-settings", "export-contacts", "export-calendar"} {
		t.Run(command, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "export")
			args := []string{"--server", s.apiURL, "--account-id", strconv.FormatUint(uint64(id), 10), "--path", path, "--json"}
			var out, errs bytes.Buffer
			var code int
			switch command {
			case "export-maildir":
				code = runExportMaildir(context.Background(), args, key, s.client, &out, &errs)
			case "export-settings":
				code = runExportSettings(context.Background(), args, key, s.client, time.Now, &out, &errs)
			default:
				code = runExportData(context.Background(), command, args, key, s.client, &out, &errs)
			}
			if code != exitFailed || out.Len() != 0 {
				t.Fatalf("exit %d, summary %q: %s", code, &out, &errs)
			}
			for _, secret := range []string{target, "secret-meeting", "secret-contact", "Secret Contact"} {
				if strings.Contains(errs.String(), secret) {
					t.Fatalf("the refusal tells of %q: %s", secret, &errs)
				}
			}
			var written []string
			_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
				if err == nil && !info.IsDir() {
					written = append(written, p)
				}
				return nil
			})
			if len(written) != 0 {
				t.Fatalf("a refused export wrote %v", written)
			}
		})
	}
}
