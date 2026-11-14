package models

import (
	"io"
	"strings"
	"testing"
	"testing/iotest"
)

// A text read through NewReadableReader comes out as ReadableText answers it, wherever the chunks
// fall, so a body streamed from disk reads as one held whole does.
func TestReadableReaderAnswersAsReadableText(t *testing.T) {
	padded := func(tail string) string { return strings.Repeat("a", readableChunk-1) + tail }
	for name, text := range map[string]string{
		"empty":                  "",
		"plain":                  "hello\r\n",
		"UTF-8":                  "Straße €100 \U0001F600\r\n",
		"Latin-1":                "Stra\xdfe \xa3100 caf\xe9\r\n",
		"NUL":                    "before\x00after",
		"a run of invalid":       "a\xff\xfe\xfdb",
		"NUL between invalid":    "\xff\x00\xff",
		"cut at the end":         "a\xe2\x82",
		"character on the chunk": padded("€after"),
		"invalid on the chunk":   padded("\xff\xfeafter"),
		"cut on the chunk":       padded("\xe2\x82after"),
		"lone tail bytes":        padded("\x80\x80\x80\x80\x80after"),
	} {
		t.Run(name, func(t *testing.T) {
			for reader, src := range map[string]io.Reader{
				"whole":     strings.NewReader(text),
				"one octet": iotest.OneByteReader(strings.NewReader(text)),
			} {
				got, err := io.ReadAll(NewReadableReader(src))
				if err != nil {
					t.Fatal(err)
				}
				if want := ReadableText(text); string(got) != want {
					t.Fatalf("%s: got %q, want %q", reader, got, want)
				}
			}
		})
	}
}
