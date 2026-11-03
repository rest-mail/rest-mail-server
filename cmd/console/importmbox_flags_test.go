package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	apitypes "github.com/rest-mail/rest-mail-server/internal/api/types"
)

func TestImportMboxPreservesKeywordsAndDeletedOnAPIWire(t *testing.T) {
	source := "From sender@example.test Mon Jan  2 15:04:05 2006\nMessage-ID: <keywords@example.test>\nSubject: flags\nStatus: R\nX-Status: D\nX-Keywords: $Forwarded $Junk\n\nbody\n\n"
	filename := filepath.Join(t.TempDir(), "flags.mbox")
	if err := os.WriteFile(filename, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	var got apitypes.ImportMessageBody
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := json.NewDecoder(request.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		_, _ = writer.Write([]byte(`{"data":{"id":7,"imported":true,"message_id":"<keywords@example.test>","folder":"Archive"}}`))
	}))
	defer server.Close()
	var out, stderr bytes.Buffer
	code := runImportMail(context.Background(), "mbox", []string{"--server", server.URL, "--account-id", "12", "--path", filename, "--folder", "Archive", "--mbox-format", "mboxrd"}, "test-key", server.Client(), &out, &stderr)
	if code != exitOK || !reflect.DeepEqual(got.Flags, []string{`\Seen`, `\Deleted`, "$Forwarded", "$Junk"}) {
		t.Fatalf("exit%d flags%v error%s", code, got.Flags, &stderr)
	}
}
