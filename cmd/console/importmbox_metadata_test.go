package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	apitypes "github.com/restmail/restmail/internal/api/types"
)

func TestImportMboxChecksMetadataBeforeCallingAPI(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"data":{"id":7,"imported":true,"message_id":"<fixture@example.test>","folder":"INBOX"}}`))
	}))
	defer server.Close()
	source := "From sender@example.test Mon Jan  2 15:04:05 2006\nSubject: private source\nX-Keywords: \nX-Keywords: private-keyword\n\nprivate body\n\n"
	path := filepath.Join(t.TempDir(), "archive.mbox")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	code := runImportMail(t.Context(), "mbox", []string{"--server", server.URL, "--account-id", "12", "--path", path, "--mbox-format", "mboxrd", "--json"},
		"private-api-key", server.Client(), &out, &stderr)
	if code != exitFailed || calls.Load() != 0 || !strings.Contains(stderr.String(), "keywords are unsupported") {
		t.Fatalf("unsupported metadata was sent: exit%d API calls%d out%s err%s", code, calls.Load(), &out, &stderr)
	}
	for _, private := range []string{"private-api-key", "private source", "private body", "private-keyword"} {
		if strings.Contains(out.String()+stderr.String(), private) {
			t.Fatal("import diagnostic disclosed source content or credentials")
		}
	}
	unchanged, err := os.ReadFile(path)
	if err != nil || string(unchanged) != source {
		t.Fatal("source archive was modified")
	}
}

func TestImportMboxSendsExactSizeMessageWithoutEnvelope(t *testing.T) {
	raw := "Subject: x\n\nbody\n"
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var message apitypes.ImportMessageBody
		if err := json.NewDecoder(r.Body).Decode(&message); err != nil || string(message.RawMessage) != raw {
			t.Errorf("raw message/framing changed: %v", err)
		}
		_, _ = w.Write([]byte(`{"data":{"id":7,"imported":true,"message_id":"<fixture@example.test>","folder":"INBOX"}}`))
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "small.mbox")
	if err := os.WriteFile(path, []byte("From sender@example.test Mon Jan  2 15:04:05 2006\n"+raw+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	code := runImportMail(t.Context(), "mbox", []string{"--server", server.URL, "--account-id", "12", "--path", path, "--mbox-format", "mboxrd",
		"--max-message-bytes", strconv.Itoa(len(raw)), "--json"}, "private-api-key", server.Client(), &out, &stderr)
	if code != exitOK || calls.Load() != 1 || !strings.Contains(out.String(), `"imported":1`) || stderr.Len() != 0 {
		t.Fatalf("exact-size message failed: exit%d API calls%d out%s err%s", code, calls.Load(), &out, &stderr)
	}
}
