package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	apitypes "github.com/rest-mail/rest-mail-server/internal/api/types"
	"github.com/rest-mail/rest-mail-server/internal/mailimport"
)

// fakeExportMessage is one message the fake public API lists and serves.
type fakeExportMessage struct {
	folder string
	listed map[string]any
	raw    []byte
}

// fakeExportServer answers the three public routes export-maildir reads, one
// message per list page so the cursor is followed.
func fakeExportServer(t *testing.T, folders []string, messages []fakeExportMessage) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer private-test-key" {
			t.Errorf("wrong request: %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/accounts/12/folders":
			var data []map[string]any
			for _, f := range folders {
				data = append(data, map[string]any{"name": f, "total": 0, "unread": 0})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
		case strings.HasPrefix(r.URL.Path, "/api/v1/accounts/12/folders/") && strings.HasSuffix(r.URL.Path, "/messages"):
			folder := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/accounts/12/folders/"), "/messages")
			if r.URL.Query().Get("include_imap_deleted") != "true" {
				t.Errorf("listing %s hides IMAP-deleted messages", folder)
			}
			var inFolder []map[string]any
			for _, m := range messages {
				if m.folder == folder {
					inFolder = append(inFolder, m.listed)
				}
			}
			start := 0
			if cursor := r.URL.Query().Get("cursor"); cursor != "" {
				if _, err := fmt.Sscan(cursor, &start); err != nil {
					t.Errorf("fake API: bad cursor %q: %v", cursor, err)
				}
			}
			page := map[string]any{"data": []map[string]any{}, "pagination": map[string]any{"has_more": false}}
			if start < len(inFolder) {
				more := start+1 < len(inFolder)
				page = map[string]any{"pagination": map[string]any{"has_more": more, "cursor": fmt.Sprint(start + 1)}, "data": inFolder[start : start+1]}
			}
			_ = json.NewEncoder(w).Encode(page)
		case strings.HasPrefix(r.URL.Path, "/api/v1/messages/") && strings.HasSuffix(r.URL.Path, "/raw"):
			for _, m := range messages {
				if r.URL.Path == fmt.Sprintf("/api/v1/messages/%v/raw", m.listed["id"]) {
					w.Header().Set("Content-Type", "message/rfc822")
					_, _ = w.Write(m.raw)
					return
				}
			}
			w.WriteHeader(http.StatusNotFound)
		default:
			t.Errorf("unexpected request %s", r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func listedMessage(id int, raw []byte, received time.Time, fields map[string]any) map[string]any {
	listed := map[string]any{"id": id, "raw_size": len(raw), "received_at": received, "body_text": "never written to the export"}
	for k, v := range fields {
		listed[k] = v
	}
	return listed
}

func TestExportMaildirReadsBackThroughImportMaildir(t *testing.T) {
	date := time.Date(2021, 3, 4, 5, 6, 7, 0, time.UTC)
	one := []byte("Message-ID: <one@example.test>\r\nSubject: one\r\n\r\nbinary \xff\x00 kept\r\n")
	two := []byte("Message-ID: <two@example.test>\nSubject: two\n\nLF only\n")
	three := []byte("Subject: three\r\n\r\nno Message-ID\r\n")
	messages := []fakeExportMessage{
		{"INBOX", listedMessage(41, one, date, map[string]any{"is_read": true, "is_answered": true, "keywords": []string{"$Forwarded", "$Junk"}}), one},
		{"INBOX", listedMessage(42, two, date.Add(time.Hour), map[string]any{"imap_deleted": true, "is_starred": true}), two},
		{"Archive/2020", listedMessage(43, three, date.Add(2*time.Hour), map[string]any{"is_draft": true, "keywords": []string{"$junk"}}), three},
	}
	srv := fakeExportServer(t, []string{"INBOX", "Archive/2020", "Sent"}, messages)
	defer srv.Close()

	root := filepath.Join(t.TempDir(), "Maildir")
	var out, stderr bytes.Buffer
	code := runExportMaildir(context.Background(), []string{"--server", srv.URL, "--account-id", "12", "--path", root, "--json"}, "private-test-key", srv.Client(), &out, &stderr)
	var summary struct{ Folders, Messages int }
	if err := json.Unmarshal(out.Bytes(), &summary); err != nil || code != exitOK || summary.Folders != 3 || summary.Messages != 3 || stderr.Len() != 0 {
		t.Fatalf("export: code=%d out=%s err=%s decode=%v", code, &out, &stderr, err)
	}
	for _, dir := range []string{"cur", "new", "tmp", ".Archive.2020/cur", ".Sent/cur"} {
		if info, err := os.Stat(filepath.Join(root, dir)); err != nil || !info.IsDir() {
			t.Fatalf("missing Maildir directory %s: %v", dir, err)
		}
	}
	if leftovers, _ := os.ReadDir(filepath.Join(root, "tmp")); len(leftovers) != 0 {
		t.Fatalf("tmp keeps %d files", len(leftovers))
	}

	var got []apitypes.ImportMessageBody
	if err := mailimport.Maildir(context.Background(), root, 1<<20, func(_ string, m apitypes.ImportMessageBody) error {
		got = append(got, m)
		return nil
	}, nil); err != nil {
		t.Fatalf("import-maildir cannot read the export: %v", err)
	}
	want := map[string]struct {
		folder string
		flags  []string
		date   time.Time
	}{
		string(one):   {"INBOX", []string{`\Answered`, `\Seen`, "$Forwarded", "$Junk"}, date},
		string(two):   {"INBOX", []string{`\Deleted`, `\Flagged`}, date.Add(time.Hour)},
		string(three): {"Archive/2020", []string{`\Draft`, "$junk"}, date.Add(2 * time.Hour)},
	}
	if len(got) != len(want) {
		t.Fatalf("read back %d messages, want %d", len(got), len(want))
	}
	for _, m := range got {
		w, ok := want[string(m.RawMessage)]
		if !ok {
			t.Fatalf("source bytes changed: %q", m.RawMessage)
		}
		flags := slices.Clone(m.Flags)
		slices.Sort(flags)
		slices.Sort(w.flags)
		if m.Folder != w.folder || !reflect.DeepEqual(flags, w.flags) || !m.InternalDate.Equal(w.date) {
			t.Errorf("%s: folder %q flags %v date %v; want %q %v %v", m.RawMessage[:20], m.Folder, flags, m.InternalDate, w.folder, w.flags, w.date)
		}
	}
}

func TestExportMaildirRefusesANonEmptyDestination(t *testing.T) {
	srv := fakeExportServer(t, []string{"INBOX"}, nil)
	defer srv.Close()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "other mail"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{"--server", srv.URL, "--account-id", "12", "--path", root}
	var out, stderr bytes.Buffer
	if code := runExportMaildir(context.Background(), args, "private-test-key", srv.Client(), &out, &stderr); code != exitUsage || !strings.Contains(stderr.String(), "--allow-non-empty") {
		t.Fatalf("non-empty destination: code=%d err=%s", code, &stderr)
	}
	stderr.Reset()
	if code := runExportMaildir(context.Background(), append(args, "--allow-non-empty"), "private-test-key", srv.Client(), &out, &stderr); code != exitOK {
		t.Fatalf("--allow-non-empty: code=%d err=%s", code, &stderr)
	}
}

func TestExportMaildirRefusesAFolderMaildirCannotName(t *testing.T) {
	raw := []byte("Subject: x\r\n\r\nx\r\n")
	srv := fakeExportServer(t, []string{"INBOX", "Clients.2020"}, []fakeExportMessage{
		{"INBOX", listedMessage(1, raw, time.Now(), nil), raw},
	})
	defer srv.Close()
	root := filepath.Join(t.TempDir(), "Maildir")
	var out, stderr bytes.Buffer
	code := runExportMaildir(context.Background(), []string{"--server", srv.URL, "--account-id", "12", "--path", root}, "private-test-key", srv.Client(), &out, &stderr)
	if code != exitFailed || !strings.Contains(stderr.String(), `"Clients.2020"`) {
		t.Fatalf("dotted folder: code=%d err=%s", code, &stderr)
	}
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Fatalf("wrote %d entries before refusing", len(entries))
	}
}

func TestExportMaildirNeedsAKeyAndAPath(t *testing.T) {
	var out, stderr bytes.Buffer
	if code := runExportMaildir(context.Background(), []string{"--server", "https://mail.example.test", "--account-id", "12", "--path", t.TempDir()}, "", http.DefaultClient, &out, &stderr); code != exitUsage {
		t.Fatalf("no key: code=%d", code)
	}
	if code := runExportMaildir(context.Background(), []string{"--server", "https://mail.example.test", "--account-id", "12"}, "k", http.DefaultClient, &out, &stderr); code != exitUsage {
		t.Fatalf("no path: code=%d", code)
	}
}
