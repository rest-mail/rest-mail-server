package handlers

import (
	"mime"
	"strings"
	"testing"
)

// TestContentDispositionAttachment proves a sender-controlled attachment filename
// is safely encoded into the Content-Disposition header (issue #202). A filename
// containing a quote must round-trip through mime.ParseMediaType rather than
// break the quoted-string, and control characters (including CR/LF header-
// injection attempts) must never appear raw in the header value.
func TestContentDispositionAttachment(t *testing.T) {
	t.Run("quoted filename round-trips", func(t *testing.T) {
		name := `in"voice".pdf`
		cd := contentDispositionAttachment(name)
		mediatype, params, err := mime.ParseMediaType(cd)
		if err != nil {
			t.Fatalf("ParseMediaType(%q) error: %v", cd, err)
		}
		if mediatype != "attachment" {
			t.Errorf("mediatype = %q, want attachment", mediatype)
		}
		if params["filename"] != name {
			t.Errorf("filename = %q, want %q (a raw quote broke the header)", params["filename"], name)
		}
	})

	t.Run("control chars never appear raw", func(t *testing.T) {
		name := "evil\r\nX-Injected: pwned\x00.txt"
		cd := contentDispositionAttachment(name)
		if cd == "" {
			t.Fatal("header value is empty")
		}
		for _, c := range cd {
			if c == '\r' || c == '\n' || c < 0x20 || c == 0x7f {
				t.Errorf("header value contains a raw control character %q: %q", c, cd)
			}
		}
	})

	t.Run("plain filename preserved", func(t *testing.T) {
		cd := contentDispositionAttachment("report.pdf")
		_, params, err := mime.ParseMediaType(cd)
		if err != nil {
			t.Fatalf("ParseMediaType error: %v", err)
		}
		if params["filename"] != "report.pdf" {
			t.Errorf("filename = %q, want report.pdf", params["filename"])
		}
		if !strings.HasPrefix(cd, "attachment") {
			t.Errorf("header = %q, want it to start with attachment", cd)
		}
	})
}
