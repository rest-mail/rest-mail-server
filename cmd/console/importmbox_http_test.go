package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	apitypes "github.com/restmail/restmail/internal/api/types"
)

func TestImportMboxPublicAPIPreservesSourceMetadata(t *testing.T) {
	raw := []byte("Message-ID: <history@example.test>\nSubject: old\nStatus: RO\nX-Status: AFT\n\n>From escaped body\nbinary\xff\x00\n")
	source := append([]byte("From old@example.test Thu Jan  2 03:04:05 +0200 2020\n"), raw...)
	source = append(source, '\n')
	path := filepath.Join(t.TempDir(), "history.mbox")
	if err := os.WriteFile(path, source, 0600); err != nil {
		t.Fatal(err)
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != "POST" || r.URL.Path != "/api/v1/accounts/12/messages/import" || r.Header.Get("Authorization") != "Bearer private-test-key" {
			t.Error("wrong public mbox destination")
		}
		var body apitypes.ImportMessageBody
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		wantRaw := bytes.Replace(raw, []byte("\n>From "), []byte("\nFrom "), 1)
		if !bytes.Equal(body.RawMessage, wantRaw) || body.Folder != "History" || !body.InternalDate.Equal(time.Date(2020, 1, 2, 1, 4, 5, 0, time.UTC)) || !reflect.DeepEqual(body.Flags, []string{`\Seen`, `\Answered`, `\Flagged`, `\Draft`}) {
			t.Error("mbox source metadata or octets changed")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"id":41,"imported":true,"message_id":"<history@example.test>","folder":"History"}}`))
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	code := runImportMail(t.Context(), "mbox", []string{"--server", server.URL, "--account-id", "12", "--path", path, "--folder", "History", "--mbox-format", "mboxrd", "--json"}, "private-test-key", server.Client(), &stdout, &stderr)
	if code != exitOK || requests != 1 || stderr.Len() != 0 {
		t.Fatalf("import failed: code=%d calls=%d out=%s err=%s", code, requests, &stdout, &stderr)
	}
	unchanged, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(unchanged, source) {
		t.Fatal("import modified the source mbox")
	}
}
