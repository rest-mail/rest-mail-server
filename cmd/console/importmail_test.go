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
	"strings"
	"testing"
	"time"

	apitypes "github.com/restmail/restmail/internal/api/types"
)

func mailImportSource(t *testing.T) (string, []byte, time.Time) {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{"cur", "new", ".Archive/cur", ".Archive/new"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0755); err != nil {
			t.Fatal(err)
		}
	}
	raw := []byte("Message-ID: <history@example.test>\r\nSubject: private source\r\n\r\nhistory\xff\x00\r\n")
	date := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	for _, name := range []string{"cur/one:2,RS", ".Archive/cur/two:2,F"} {
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, date, date); err != nil {
			t.Fatal(err)
		}
	}
	return root, raw, date
}

func TestImportMaildirPublicAPIAndResumableSummary(t *testing.T) {
	root, raw, date := mailImportSource(t)
	var received []apitypes.ImportMessageBody
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/accounts/12/messages/import" || r.Header.Get("Authorization") != "Bearer private-test-key" {
			t.Errorf("wrong public import request: %s %s", r.Method, r.URL.Path)
		}
		var message apitypes.ImportMessageBody
		if err := json.NewDecoder(r.Body).Decode(&message); err != nil {
			t.Error(err)
		}
		received = append(received, message)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"data":{"id":%d,"imported":%t,"message_id":"<history@example.test>","folder":%q}}`, 40+(len(received)-1)%2, len(received) <= 2, message.Folder)
	}))
	defer srv.Close()
	for _, want := range []struct{ imported, skipped int }{{2, 0}, {0, 2}} {
		var out, stderr bytes.Buffer
		code := runImportMail(context.Background(), "maildir", []string{"--server", srv.URL, "--account-id", "12", "--path", root, "--json"}, "private-test-key", srv.Client(), &out, &stderr)
		var summary struct{ Messages, Imported, Skipped int }
		if err := json.Unmarshal(out.Bytes(), &summary); err != nil || code != exitOK || summary.Messages != 2 || summary.Imported != want.imported || summary.Skipped != want.skipped || stderr.Len() != 0 {
			t.Fatalf("summary: code=%d out=%s err=%s decode=%v", code, &out, &stderr, err)
		}
	}
	if len(received) != 4 {
		t.Fatalf("requests=%d", len(received))
	}
	for _, message := range received {
		if !bytes.Equal(message.RawMessage, raw) || !message.InternalDate.Equal(date) {
			t.Fatalf("source bytes/date changed: %+v", message)
		}
		flags := []string{`\Answered`, `\Seen`}
		if message.Folder == "Archive" {
			flags = []string{`\Flagged`}
		} else if message.Folder != "INBOX" {
			t.Fatalf("unexpected destination: %q", message.Folder)
		}
		if !reflect.DeepEqual(message.Flags, flags) {
			t.Fatalf("source flags changed: %v", message.Flags)
		}
	}
}

func TestImportMailRefusesRedirectsAndRedactsSourceAndCredential(t *testing.T) {
	root, raw, _ := mailImportSource(t)
	targetRequests := 0
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { targetRequests++ }))
	defer target.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	var out, stderr bytes.Buffer
	code := runImportMail(context.Background(), "maildir", []string{"--server", srv.URL, "--account-id", "12", "--path", root, "--json"}, "private-test-key", srv.Client(), &out, &stderr)
	if code != exitFailed || targetRequests != 0 || out.Len() != 0 || !strings.Contains(stderr.String(), "HTTP 307") {
		t.Fatalf("redirect was not safely refused: code=%d target=%d out=%s err=%s", code, targetRequests, &out, &stderr)
	}
	for _, secret := range []string{"private-test-key", string(raw), "private source"} {
		if strings.Contains(out.String()+stderr.String(), secret) {
			t.Fatal("source or credential appeared in command output")
		}
	}
}

func TestImportMboxDryRunAndExplicitFormat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "archive.mbox")
	source := "From sender@example.test Mon Jan  2 15:04:05 2006\nMessage-ID: <old@example.test>\nSubject: historical\n\n>From old body\n\n"
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"--account-id", "12", "--path", path, "--dry-run", "--json"}
	var out, stderr bytes.Buffer
	if code := runImportMail(context.Background(), "mbox", args, "", nil, &out, &stderr); code != exitUsage || !strings.Contains(stderr.String(), "mbox-format") {
		t.Fatalf("ambiguous escaping accepted: code=%d out=%s err=%s", code, &out, &stderr)
	}
	out.Reset()
	stderr.Reset()
	if code := runImportMail(context.Background(), "mbox", append(args, "--mbox-format", "mboxrd"), "", nil, &out, &stderr); code != exitOK || !strings.Contains(out.String(), `"messages":1`) || !strings.Contains(out.String(), `"dry_run":true`) || stderr.Len() != 0 {
		t.Fatalf("dry run: code=%d out=%s err=%s", code, &out, &stderr)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != source {
		t.Fatalf("source changed: %q %v", got, err)
	}
}
