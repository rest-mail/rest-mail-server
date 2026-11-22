package mailimport

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestThunderbirdMetadataEscapingAndLineEndings(t *testing.T) {
	for _, newline := range []string{"\n", "\r\n"} {
		t.Run(map[string]string{"\n": "LF", "\r\n": "CRLF"}[newline], func(t *testing.T) {
			source := strings.Join([]string{"From - Tue Jan  2 03:04:05 2024", "Message-ID: <synthetic@example.test>", "Date: Mon, 01 Jan 2024 12:00:00 +0200", "X-Mozilla-Status: 0007", "X-Mozilla-Status2: 00200000", "X-Mozilla-Keys: $label1", "", ">From escaped", ">>From nested", "", "From - Tue Jan  2 03:04:05 2024", "X-Mozilla-Status: 0009", "Date: Mon, 01 Jan 2024 12:00:00 +0200", "", "expunged", ""}, newline)
			var messages []Message
			expunged := 0
			err := Thunderbird(context.Background(), strings.NewReader(source), "Archive/2024", 1<<20, func(_ string, message Message) error { messages = append(messages, message); return nil }, func() { expunged++ })
			if err != nil {
				t.Fatal(err)
			}
			if len(messages) != 1 || expunged != 1 {
				t.Fatalf("messages=%d expunged=%d", len(messages), expunged)
			}
			message := messages[0]
			if !reflect.DeepEqual(message.Flags, []string{`\Seen`, `\Answered`, `\Flagged`, `\Deleted`, "$label1"}) {
				t.Fatalf("flags=%v", message.Flags)
			}
			if bytes.Contains(message.RawMessage, []byte("X-Mozilla")) || !bytes.Contains(message.RawMessage, []byte("From escaped"+newline+">From nested")) {
				t.Fatal("metadata removal or escaping failed")
			}
			if message.Folder != "Archive/2024" || !message.InternalDate.Equal(time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)) {
				t.Fatal("folder or separator date lost")
			}
		})
	}
}

func TestThunderbirdDateFallbackAndFlagCombinations(t *testing.T) {
	for _, item := range []struct {
		status string
		flags  []string
	}{{"0000", []string{}}, {"0001", []string{`\Seen`}}, {"0002", []string{`\Answered`}}, {"0004", []string{`\Flagged`}}} {
		t.Run(item.status, func(t *testing.T) {
			source := "From - invalid-date\nDate: Mon, 01 Jan 2024 12:00:00 +0200\nX-Mozilla-Status: " + item.status + "\n\nbody\n"
			calls := 0
			err := Thunderbird(context.Background(), strings.NewReader(source), "INBOX", 1<<20, func(_ string, message Message) error {
				calls++
				if !reflect.DeepEqual(message.Flags, item.flags) || message.InternalDate.Hour() != 12 {
					t.Fatal("metadata mismatch")
				}
				return nil
			}, nil)
			if err != nil || calls != 1 {
				t.Fatalf("calls=%d error=%v", calls, err)
			}
		})
	}
}

func TestThunderbirdRejectsInvalidMetadataAndOversize(t *testing.T) {
	for _, source := range []string{"From - bad\nDate: bad\n\nbody\n", "From - Tue Jan  2 03:04:05 2024\nX-Mozilla-Status: invalid\n\nbody\n", "From - Tue Jan  2 03:04:05 2024\nX-Mozilla-Status: 0400\n\nbody\n", "From - Tue Jan  2 03:04:05 2024\n\n" + strings.Repeat("x", 100)} {
		if err := Thunderbird(context.Background(), strings.NewReader(source), "INBOX", 96, func(string, Message) error { t.Fatal("unexpected import"); return nil }, nil); err == nil {
			t.Fatal("expected refusal")
		}
	}
}

func TestThunderbirdSkipsOversizedExpungedBodiesWithoutBuffering(t *testing.T) {
	source := "From - Tue Jan  2 03:04:05 2024\nX-Mozilla-Status: 0008\n\n" + strings.Repeat("x", 2<<20) + "\nFrom - Tue Jan  2 03:04:05 2024\nMessage-ID: <next@example.test>\n\nkept\n"
	expunged, imported := 0, 0
	err := Thunderbird(context.Background(), strings.NewReader(source), "INBOX", 128, func(_ string, message Message) error {
		imported++
		if !bytes.Contains(message.RawMessage, []byte("kept")) {
			t.Fatal("next message lost")
		}
		return nil
	}, func() { expunged++ })
	if err != nil || expunged != 1 || imported != 1 {
		t.Fatalf("imported=%d expunged=%d error=%v", imported, expunged, err)
	}
}
