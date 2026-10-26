package mailimport

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	apitypes "github.com/restmail/restmail/internal/api/types"
)

func sourceMaildir(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{"cur", "new", "tmp"} {
		if err := os.Mkdir(filepath.Join(root, name), 0755); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestMaildirPreservesFoldersFlagsDatesAndOctets(t *testing.T) {
	root := sourceMaildir(t)
	for _, sub := range []string{"cur", "new"} {
		if err := os.MkdirAll(filepath.Join(root, ".Archive.2020", sub), 0755); err != nil {
			t.Fatal(err)
		}
	}
	raw := []byte("Message-ID: <history@example.test>\r\nSubject: original\r\n\r\nbody\xff\x00\r\n")
	date := time.Date(2020, 4, 3, 2, 1, 0, 0, time.UTC)
	for _, name := range []string{"cur/first:2,RSF", ".Archive.2020/cur/second:2,D"} {
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, date, date); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "tmp", "unfinished"), bytes.Repeat([]byte("x"), 2048), 0600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		got := map[string]apitypes.ImportMessageBody{}
		err := Maildir(context.Background(), root, 1024, func(_ string, message apitypes.ImportMessageBody) error {
			got[message.Folder] = message
			return nil
		})
		if err != nil || len(got) != 2 {
			t.Fatalf("read: %v, folders=%v", err, got)
		}
		for folder, flags := range map[string][]string{"INBOX": {`\Answered`, `\Seen`, `\Flagged`}, "Archive/2020": {`\Draft`}} {
			message := got[folder]
			if !bytes.Equal(message.RawMessage, raw) || !message.InternalDate.Equal(date) || !reflect.DeepEqual(message.Flags, flags) {
				t.Fatalf("source metadata changed for %s: %+v", folder, message)
			}
		}
	}
	stored, err := os.ReadFile(filepath.Join(root, "cur", "first:2,RSF"))
	if err != nil || !bytes.Equal(stored, raw) {
		t.Fatalf("source was modified: %q %v", stored, err)
	}
}

func TestMaildirRefusesUnsupportedAndUnsafeSources(t *testing.T) {
	for _, flag := range []string{"P", "a", "T"} {
		root := sourceMaildir(t)
		if err := os.WriteFile(filepath.Join(root, "cur", "message:2,"+flag), []byte("Subject: x\n\nx\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := Maildir(context.Background(), root, 1024, func(string, apitypes.ImportMessageBody) error { t.Fatal("unsupported flag reached import"); return nil }); err == nil {
			t.Fatalf("silently discarded flag %s", flag)
		}
	}
	root := sourceMaildir(t)
	if err := os.WriteFile(filepath.Join(root, "cur", "large"), bytes.Repeat([]byte("x"), 2048), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Maildir(context.Background(), root, 1024, func(string, apitypes.ImportMessageBody) error { t.Fatal("oversized source reached import"); return nil }); err == nil {
		t.Fatal("oversized message accepted")
	}
	root = sourceMaildir(t)
	if err := os.Symlink(t.TempDir(), filepath.Join(root, ".External")); err != nil {
		t.Fatal(err)
	}
	if err := Maildir(context.Background(), root, 1024, func(string, apitypes.ImportMessageBody) error { t.Fatal("external source reached import"); return nil }); err == nil {
		t.Fatal("source symlink accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Maildir(ctx, sourceMaildir(t), 1024, func(string, apitypes.ImportMessageBody) error { t.Fatal("canceled import continued"); return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}

func TestMboxPreservesMetadataAndExplicitEscaping(t *testing.T) {
	for _, tc := range []struct{ format, expected string }{
		{"rfc4155", ">From original\n>>From quoted\n"},
		{"mboxo", "From original\n>>From quoted\n"},
		{"mboxrd", "From original\n>From quoted\n"},
	} {
		t.Run(tc.format, func(t *testing.T) {
			first := "Message-ID: <one@example.test>\nStatus: RO\nX-Status: AFT\nSubject: first\n\n>From original\n>>From quoted\nFrom ordinary body text\n\n"
			second := "Message-ID: <two@example.test>\r\nSubject: second\r\n\r\nbody\xff\x00\r\n\r\n"
			source := "From sender@example.test Mon Jan  2 15:04:05 2006\n" + first + "From sender@example.test Tue Jan  3 12:00:00 2006\r\n" + second
			var got []apitypes.ImportMessageBody
			err := Mbox(context.Background(), strings.NewReader(source), "Archive", tc.format, 4096, func(_ string, message apitypes.ImportMessageBody) error { got = append(got, message); return nil })
			if err != nil || len(got) != 2 {
				t.Fatalf("read: %v, messages=%d", err, len(got))
			}
			want := "Message-ID: <one@example.test>\nStatus: RO\nX-Status: AFT\nSubject: first\n\n" + tc.expected + "From ordinary body text\n"
			if string(got[0].RawMessage) != want || string(got[1].RawMessage) != strings.TrimSuffix(second, "\r\n") {
				t.Fatalf("source bytes changed: first=%q second=%q", got[0].RawMessage, got[1].RawMessage)
			}
			if !got[0].InternalDate.Equal(time.Date(2006, 1, 2, 15, 4, 5, 0, time.UTC)) || got[0].Folder != "Archive" || !reflect.DeepEqual(got[0].Flags, []string{`\Seen`, `\Answered`, `\Flagged`, `\Draft`}) {
				t.Fatalf("mbox metadata changed: %+v", got[0])
			}
		})
	}
}

func TestMboxBoundsFramingAndRefusesUnsupportedMetadata(t *testing.T) {
	prefix := "From sender@example.test Mon Jan  2 15:04:05 2006\n"
	for _, source := range []string{
		"Subject: not an mbox\n\nbody\n",
		prefix + "Subject: x\nContent-Length: 4\n\nbody\n\n",
		prefix + "Subject: x\nX-Status: D\n\nbody\n\n",
		prefix + "Subject: x\nX-Status: Z\n\nbody\n\n",
		prefix + "Subject: x\nStatus: Z\n\nbody\n\n",
		"From  Mon Jan  2 15:04:05 2006\nSubject: x\n\nbody\n",
		prefix + "Subject: x\nX-Keywords: custom\n\nbody\n\n",
		prefix + "Subject: metadata\nX-IMAP: 1 2\n\nbody\n\n",
	} {
		if err := Mbox(context.Background(), strings.NewReader(source), "INBOX", "mboxrd", 4096, func(string, apitypes.ImportMessageBody) error {
			t.Fatal("unsupported source reached import")
			return nil
		}); err == nil {
			t.Fatalf("unsupported source accepted: %q", source)
		}
	}
	long := prefix + "Subject: large line\n\n" + strings.Repeat("x", 100000) + "\n\n"
	count := 0
	if err := Mbox(context.Background(), strings.NewReader(long), "INBOX", "mboxrd", 128000, func(string, apitypes.ImportMessageBody) error { count++; return nil }); err != nil || count != 1 {
		t.Fatalf("line above Scanner's default limit: count=%d error=%v", count, err)
	}
	if err := Mbox(context.Background(), strings.NewReader(long), "INBOX", "mboxrd", 1024, func(string, apitypes.ImportMessageBody) error { t.Fatal("oversized source reached import"); return nil }); err == nil {
		t.Fatal("oversized line accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Mbox(ctx, strings.NewReader(prefix), "INBOX", "mboxrd", 1024, func(string, apitypes.ImportMessageBody) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}
