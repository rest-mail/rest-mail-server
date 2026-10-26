package handlers

import (
	"bytes"
	"encoding/base64"
	"testing"
	"time"

	rmime "github.com/restmail/restmail/internal/mime"
	"github.com/restmail/restmail/internal/pipeline"
)

func TestForwardMIMEPreservesBinaryEmptyAndInlineParts(t *testing.T) {
	parts := []pipeline.Attachment{
		{Filename: "invoice.bin", ContentType: "application/octet-stream", Disposition: "attachment", Content: base64.StdEncoding.EncodeToString([]byte("invoice\xff\x00")), Size: 9},
		{Filename: "empty.txt", ContentType: "text/plain", Disposition: "attachment", Size: 0},
		{Filename: "image.png", ContentType: "image/png", Disposition: "inline", ContentID: "original-image", Content: base64.StdEncoding.EncodeToString([]byte("image")), Size: 5},
	}
	raw, err := buildOutboundRaw(outboundMessage{From: "user@example.test", To: []string{"target@example.test"}, Subject: "Fwd: receipt", BodyText: "forwarded receipt", BodyHTML: `<p>receipt<img src="cid:original-image"></p>`, MessageID: "<forward@example.test>", Date: time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC), Attachments: parts})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := rmime.Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Attachments) != 2 || len(parsed.Inline) != 1 {
		t.Fatalf("parts lost: attachments=%+v inline=%+v", parsed.Attachments, parsed.Inline)
	}
	if parsed.Inline[0].ContentID != "original-image" {
		t.Fatal("inline Content-ID changed")
	}
	for _, want := range parts {
		found := false
		for _, got := range append(parsed.Attachments, parsed.Inline...) {
			if got.Filename != want.Filename {
				continue
			}
			found = true
			a, err := base64.StdEncoding.DecodeString(got.Content)
			if err != nil {
				t.Fatal(err)
			}
			b, err := base64.StdEncoding.DecodeString(want.Content)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(a, b) || got.ContentType != want.ContentType {
				t.Fatal("forward changed part bytes/type")
			}
		}
		if !found {
			t.Fatalf("missing part %s", want.Filename)
		}
	}
}
