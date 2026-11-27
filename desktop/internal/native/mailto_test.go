package native

import (
	"strings"
	"testing"
)

func TestParseMailtoFollowsRFC6068(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want Mailto
	}{
		{"single address", "mailto:chris@example.com", Mailto{To: "chris@example.com"}},
		{"scheme ignores case", "MAILTO:chris@example.com", Mailto{To: "chris@example.com"}},
		{"several path addresses", "mailto:a@example.com,b@example.com", Mailto{To: "a@example.com, b@example.com"}},
		{"percent-encoded path", "mailto:%22not%40me%22@example.org", Mailto{To: `"not@me"@example.org`}},
		{"plus stays a plus", "mailto:list+tag@example.com?subject=1+1", Mailto{To: "list+tag@example.com", Subject: "1+1"}},
		{"header fields", "mailto:a@example.com?cc=b@example.com&bcc=c@example.com&subject=Hello%20there&body=Line%201%0D%0ALine%202",
			Mailto{To: "a@example.com", Cc: "b@example.com", Bcc: "c@example.com", Subject: "Hello there", Body: "Line 1\nLine 2"}},
		{"to field joins path", "mailto:a@example.com?to=b@example.com", Mailto{To: "a@example.com, b@example.com"}},
		{"no path address", "mailto:?to=a@example.com&subject=Hi", Mailto{To: "a@example.com", Subject: "Hi"}},
		{"field names ignore case", "mailto:a@example.com?SUBJECT=Hi&Body=Text", Mailto{To: "a@example.com", Subject: "Hi", Body: "Text"}},
		{"in-reply-to", "mailto:list@example.org?In-Reply-To=%3C3469A91.D10AF4C@example.com%3E", Mailto{To: "list@example.org", InReplyTo: "<3469A91.D10AF4C@example.com>"}},
		{"unknown fields ignored", "mailto:a@example.com?X-Mailer=evil&from=boss@example.com&subject=Hi", Mailto{To: "a@example.com", Subject: "Hi"}},
		{"fragment ignored", "mailto:a@example.com?subject=Hi#section", Mailto{To: "a@example.com", Subject: "Hi"}},
		{"unicode subject", "mailto:a@example.com?subject=Gr%C3%BC%C3%9Fe", Mailto{To: "a@example.com", Subject: "Grüße"}},
		{"empty link", "mailto:", Mailto{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseMailto(tc.raw)
			if err != nil {
				t.Fatalf("ParseMailto(%q): %v", tc.raw, err)
			}
			if got != tc.want {
				t.Fatalf("ParseMailto(%q) = %+v, want %+v", tc.raw, got, tc.want)
			}
		})
	}
}

func TestParseMailtoRejectsUnsafeLinks(t *testing.T) {
	many := "mailto:" + strings.Repeat("a@example.com,", MaxMailtoRecipients+1)
	cases := map[string]string{
		"other scheme":              "https://example.com",
		"bad percent-encoding":      "mailto:a@example.com?subject=%zz",
		"invalid address":           "mailto:not-an-address",
		"line break in subject":     "mailto:a@example.com?subject=Hi%0D%0ABcc:%20victim@example.com",
		"line break in address":     "mailto:a@example.com%0D%0ABcc:victim@example.com",
		"line break in in-reply-to": "mailto:a@example.com?in-reply-to=%3Cx@y%3E%0ABcc:%20v@example.com",
		"too many recipients":       many,
		"too long":                  "mailto:a@example.com?body=" + strings.Repeat("x", MaxMailtoLength),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if got, err := ParseMailto(raw); err == nil {
				t.Fatalf("ParseMailto accepted %q as %+v", raw, got)
			}
		})
	}
}

func TestMailtoArgumentFindsTheLink(t *testing.T) {
	if got := MailtoArgument([]string{"--flag", "Mailto:a@example.com"}); got != "Mailto:a@example.com" {
		t.Fatalf("MailtoArgument = %q", got)
	}
	if got := MailtoArgument([]string{"--flag", "https://example.com", "mail"}); got != "" {
		t.Fatalf("MailtoArgument without a link = %q", got)
	}
}
