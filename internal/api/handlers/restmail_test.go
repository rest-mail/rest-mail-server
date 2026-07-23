package handlers

import (
	"testing"

	"github.com/restmail/restmail/internal/pipeline"
)

// TestExtractBodyParts_MediaTypeWithParams ensures body-part matching keys off
// the media type, not the full Content-Type string. Parsed and pipeline-emitted
// parts commonly carry a charset parameter ("text/plain; charset=utf-8"); an
// exact "text/plain" match would miss those and drop the body content.
func TestExtractBodyParts_MediaTypeWithParams(t *testing.T) {
	body := pipeline.Body{
		ContentType: "multipart/alternative",
		Parts: []pipeline.Body{
			{ContentType: "text/plain; charset=utf-8", Content: "hello text"},
			{ContentType: "text/html; charset=UTF-8", Content: "<p>hello html</p>"},
		},
	}

	text, html := extractBodyParts(body)
	if text != "hello text" {
		t.Errorf("text = %q, want %q", text, "hello text")
	}
	if html != "<p>hello html</p>" {
		t.Errorf("html = %q, want %q", html, "<p>hello html</p>")
	}
}

// TestExtractBodyParts_SinglePart covers a top-level (non-multipart) body.
func TestExtractBodyParts_SinglePart(t *testing.T) {
	text, html := extractBodyParts(pipeline.Body{
		ContentType: "text/plain",
		Content:     "just text",
	})
	if text != "just text" {
		t.Errorf("text = %q, want %q", text, "just text")
	}
	if html != "" {
		t.Errorf("html = %q, want empty", html)
	}
}

func TestMediaType(t *testing.T) {
	cases := map[string]string{
		"text/plain":                "text/plain",
		"text/plain; charset=utf-8": "text/plain",
		"  TEXT/HTML ; x=y":         "text/html",
		"multipart/alternative":     "multipart/alternative",
	}
	for in, want := range cases {
		if got := mediaType(in); got != want {
			t.Errorf("mediaType(%q) = %q, want %q", in, got, want)
		}
	}
}
