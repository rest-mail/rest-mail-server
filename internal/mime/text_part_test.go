package mime

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"
)

// streamedText parses message with TextPart, keeping the parts in the order they arrive.
func streamedText(t *testing.T, message string) (kept map[string]string, seen []string) {
	t.Helper()
	kept = map[string]string{}
	_, err := ParseStream(context.Background(), strings.NewReader(message), StreamOptions{
		Store: func(_ context.Context, part io.Reader) (StoredPart, error) {
			_, err := io.Copy(io.Discard, part)
			return StoredPart{}, err
		},
		MaxText: 1,
		TextPart: func(mediaType string, part io.Reader) error {
			content, err := io.ReadAll(part)
			if err != nil {
				return err
			}
			seen = append(seen, mediaType)
			if kept[mediaType] == "" {
				kept[mediaType] = string(content)
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return kept, seen
}

// TestTextPartFindsWhatTextAndHTMLFinds holds the parts handed over to the two fields Parse and
// TextAndHTML pick from the same message.
func TestTextPartFindsWhatTextAndHTMLFinds(t *testing.T) {
	for name, message := range map[string]string{
		"single":                 "From: a@x.test\r\nContent-Type: text/plain\r\n\r\nbody",
		"params lost":            "From: a@x.test\r\nContent-Type: TEXT/HTML; charset\r\n\r\n<b>x</b>",
		"bad single part":        "From: a@x.test\r\nContent-Type: text/plain\r\nContent-Transfer-Encoding: base64\r\n\r\nQUJD!!!",
		"bad part left out":      "From: a@x.test\r\nContent-Type: multipart/mixed; boundary=m\r\n\r\n--m\r\nContent-Type: text/plain\r\nContent-Transfer-Encoding: base64\r\n\r\nQUJD!!!\r\n--m\r\nContent-Type: text/plain\r\n\r\nsecond\r\n--m--\r\n",
		"attachment is not body": "From: a@x.test\r\nContent-Type: multipart/mixed; boundary=m\r\n\r\n--m\r\nContent-Type: text/plain\r\nContent-Disposition: attachment; filename=a.txt\r\n\r\nno\r\n--m\r\nContent-Type: text/html\r\n\r\nyes\r\n--m--\r\n",
		"nested":                 "From: a@x.test\r\nContent-Type: multipart/mixed; boundary=m\r\n\r\n--m\r\nContent-Type: multipart/alternative; boundary=a\r\n\r\n--a\r\nContent-Type: text/plain\r\n\r\nplain\r\n--a\r\nContent-Type: text/html\r\n\r\nhtml\r\n--a--\r\n--m--\r\n",
	} {
		t.Run(name, func(t *testing.T) {
			email, err := Parse([]byte(message))
			if err != nil {
				t.Fatal(err)
			}
			wantText, wantHTML := TextAndHTML(email.Body)
			kept, _ := streamedText(t, message)
			if kept["text/plain"] != wantText || kept["text/html"] != wantHTML {
				t.Fatalf("text, html = %q, %q, want %q, %q", kept["text/plain"], kept["text/html"], wantText, wantHTML)
			}
		})
	}
}

// TestTextPartFailureIsAnError keeps the message from being taken without a part the caller could
// not keep, as a failure to store an attachment does.
func TestTextPartFailureIsAnError(t *testing.T) {
	boom := errors.New("disk full")
	_, err := ParseStream(context.Background(), strings.NewReader("From: a@x.test\r\nContent-Type: text/plain\r\n\r\nbody"), StreamOptions{
		Store:    func(context.Context, io.Reader) (StoredPart, error) { return StoredPart{}, nil },
		MaxText:  1,
		TextPart: func(string, io.Reader) error { return boom },
	})
	if !errors.Is(err, boom) {
		t.Fatalf("error = %v, want %v", err, boom)
	}
}

// TestCountReceivedStopsAtTheHeaderBlock reads no further than it has to, and reports a failure
// reading before the block's end.
func TestCountReceivedStopsAtTheHeaderBlock(t *testing.T) {
	reader := io.MultiReader(strings.NewReader("Received: a\r\n\r\n"), iotest.ErrReader(errors.New("past the header block")))
	if count, err := CountReceived(reader); err != nil || count != 1 {
		t.Fatalf("CountReceived = %d, %v, want 1 and no error", count, err)
	}
	boom := errors.New("disk failure")
	if _, err := CountReceived(io.MultiReader(strings.NewReader("Received: a\r\n"), iotest.ErrReader(boom))); !errors.Is(err, boom) {
		t.Fatalf("error = %v, want %v", err, boom)
	}
}
