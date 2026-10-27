package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	apitypes "github.com/restmail/restmail/internal/api/types"
	"github.com/restmail/restmail/internal/mailimport"
)

func imapImportMessages() []apitypes.ImportMessageBody {
	raw := []byte("Message-ID: <history@example.test>\r\nSubject: private-source-content\r\n\r\n\xff\x00body\r\n")
	date := time.Date(2020, 1, 2, 3, 4, 5, 0, time.FixedZone("source", -5*3600))
	return []apitypes.ImportMessageBody{
		{RawMessage: raw, Folder: "INBOX", Flags: []string{`\Seen`, `\Answered`}, InternalDate: date},
		{RawMessage: raw, Folder: "Archive/2020", Flags: []string{`\Flagged`}, InternalDate: date},
	}
}

func TestImportIMAPPublicAPIAndResumableSummary(t *testing.T) {
	seen := map[string]bool{}
	var mu sync.Mutex
	var received []apitypes.ImportMessageBody
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/accounts/12/messages/import" || r.Header.Get("Authorization") != "Bearer destination-key-never-log" {
			t.Error("wrong import operation or destination credential")
		}
		var body apitypes.ImportMessageBody
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		received = append(received, body)
		imported := !seen[body.Folder]
		seen[body.Folder] = true
		fmt.Fprintf(w, `{"data":{"id":40,"message_id":"<history@example.test>","folder":%q,"imported":%t}}`, body.Folder, imported)
	}))
	defer srv.Close()
	reader := func(ctx context.Context, options mailimport.IMAPOptions, limit int64, visit mailimport.Visit) error {
		if options.Address != "old.example.test:1993" || options.Username != "user*master" || options.Password != "source-password-never-log" || limit != 10<<20 || options.TLSConfig != nil {
			t.Error("source settings or default certificate verification changed")
		}
		for _, body := range imapImportMessages() {
			if err := visit("IMAP UID 10", body); err != nil {
				return err
			}
		}
		return nil
	}
	for _, want := range []struct{ imported, skipped int }{{2, 0}, {0, 2}} {
		var out, stderr bytes.Buffer
		code := runImportIMAPSource(context.Background(), []string{"--source", "imaps://old.example.test:1993", "--source-user", "user*master", "--server", srv.URL, "--account-id", "12", "--json"}, "destination-key-never-log", "source-password-never-log", srv.Client(), &out, &stderr, reader)
		var summary struct{ Messages, Imported, Skipped int }
		if err := json.Unmarshal(out.Bytes(), &summary); err != nil || code != exitOK || summary.Messages != 2 || summary.Imported != want.imported || summary.Skipped != want.skipped || stderr.Len() != 0 {
			t.Fatalf("summary: code=%d out=%s err=%s decode=%v", code, &out, &stderr, err)
		}
		for _, secret := range []string{"destination-key-never-log", "source-password-never-log", "private-source-content"} {
			if strings.Contains(out.String()+stderr.String(), secret) {
				t.Fatal("source content or credentials leaked in output")
			}
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(received) != 4 {
		t.Fatalf("requests=%d", len(received))
	}
	for i, message := range received {
		want := imapImportMessages()[i%2]
		_, offset := message.InternalDate.Zone()
		if !reflect.DeepEqual(message.RawMessage, want.RawMessage) || !reflect.DeepEqual(message.Flags, want.Flags) || message.Folder != want.Folder || !message.InternalDate.Equal(want.InternalDate) || offset != -5*3600 {
			t.Fatal("source octets, folder, flags or internal date changed on the API wire")
		}
	}
}

func TestImportIMAPDryRunAndPartialFailure(t *testing.T) {
	for _, dry := range []bool{false, true} {
		t.Run(fmt.Sprint(dry), func(t *testing.T) {
			var requests atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if requests.Add(1) == 2 {
					http.Error(w, "destination-key-never-log private-source-content", http.StatusInsufficientStorage)
					return
				}
				fmt.Fprint(w, `{"data":{"id":1,"message_id":"<history@example.test>","folder":"INBOX","imported":true}}`)
			}))
			defer srv.Close()
			args := []string{"--source", "imaps://old.example.test", "--source-user", "alice", "--account-id", "12", "--json"}
			token := ""
			if dry {
				args = append(args, "--dry-run")
			} else {
				args = append(args, "--server", srv.URL)
				token = "destination-key-never-log"
			}
			reader := func(ctx context.Context, options mailimport.IMAPOptions, limit int64, visit mailimport.Visit) error {
				if options.Address != "old.example.test:993" {
					t.Error("default source TLS port changed")
				}
				for _, body := range imapImportMessages() {
					if err := visit("IMAP UID 10", body); err != nil {
						return err
					}
				}
				return nil
			}
			var out, stderr bytes.Buffer
			code := runImportIMAPSource(context.Background(), args, token, "source-password-never-log", srv.Client(), &out, &stderr, reader)
			if dry {
				if code != exitOK || requests.Load() != 0 || !strings.Contains(out.String(), `"dry_run":true`) || stderr.Len() != 0 {
					t.Fatalf("dry-run wrote to API: code=%d requests=%d out=%s err=%s", code, requests.Load(), &out, &stderr)
				}
			} else if code != exitFailed || requests.Load() != 2 || out.Len() != 0 || !strings.Contains(stderr.String(), "completed 1 imports, skipped 0 existing copies") || !strings.Contains(stderr.String(), "HTTP 507") {
				t.Fatalf("partial progress lost: code=%d requests=%d out=%s err=%s", code, requests.Load(), &out, &stderr)
			}
			for _, secret := range []string{"destination-key-never-log", "source-password-never-log", "private-source-content"} {
				if strings.Contains(out.String()+stderr.String(), secret) {
					t.Fatal("credential, message or server body leaked")
				}
			}
		})
	}
}

func TestImportIMAPRejectsUnsafeSettings(t *testing.T) {
	for _, extra := range [][]string{
		{"--source", "imap://old.example.test"},
		{"--source", "imaps://alice:source-password-never-log@old.example.test"},
		{"--source", "imaps://old.example.test?password=source-password-never-log"},
		{"--source", "imaps://old.example.test:70000"},
		{"--source", "imaps://old.example.test/path"},
		{"--source-user", ""}, {"--account-id", "0"},
		{"--max-message-bytes", "0"}, {"--password", "source-password-never-log"},
	} {
		t.Run(fmt.Sprint(extra), func(t *testing.T) {
			args := append([]string{"--source", "imaps://old.example.test", "--source-user", "alice", "--account-id", "12", "--dry-run", "--json"}, extra...)
			var out, stderr bytes.Buffer
			code := runImportIMAPSource(context.Background(), args, "destination-key-never-log", "source-password-never-log", http.DefaultClient, &out, &stderr, func(context.Context, mailimport.IMAPOptions, int64, mailimport.Visit) error {
				t.Fatal("invalid settings contacted source")
				return nil
			})
			if code != exitUsage || out.Len() != 0 || stderr.Len() == 0 || strings.Contains(stderr.String(), "source-password-never-log") || strings.Contains(stderr.String(), "destination-key-never-log") {
				t.Fatalf("unsafe settings or output: code=%d out=%s err=%s", code, &out, &stderr)
			}
		})
	}
}

func TestImportIMAPSubcommandHelp(t *testing.T) {
	t.Setenv("RESTMAIL_IMAP_PASSWORD", "source-password-never-log")
	t.Setenv("RESTMAIL_API_KEY", "destination-key-never-log")
	var out, stderr bytes.Buffer
	code, handled := runSubcommand([]string{"restmail", "import-imap", "--help"}, &out, &stderr)
	if !handled || code != exitOK || stderr.Len() != 0 || !strings.Contains(out.String(), "BODY.PEEK") || strings.Contains(out.String(), "source-password-never-log") || strings.Contains(out.String(), "destination-key-never-log") {
		t.Fatalf("subcommand/help wiring: handled=%t code=%d out=%s err=%s", handled, code, &out, &stderr)
	}
}
