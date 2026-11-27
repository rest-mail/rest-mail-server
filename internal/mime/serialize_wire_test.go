package mime

import (
	"encoding/base64"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/pipeline"
)

const wireDate = "Fri, 27 Nov 2026 20:00:00 +0000"

func serializeWireCases() []struct {
	name  string
	email *pipeline.EmailJSON
	wire  string
} {
	const plain = "text/plain; charset=utf-8"
	text := pipeline.Body{Content: "Grüße", TransferEncoding: "8bit"}
	calendar := pipeline.Body{
		ContentType: "text/calendar; method=REPLY", TransferEncoding: "base64",
		Content: "QkVHSU46VkNBTEVOREFSDQpFTkQ6VkNBTEVOREFSDQo=",
	}
	image := pipeline.Attachment{
		ContentType: "image/png", Disposition: "inline", ContentID: "<chart>", Content: "AAEC",
	}
	file := pipeline.Attachment{
		Filename: "file.bin", ContentType: "application/octet-stream",
		Content: base64.StdEncoding.EncodeToString(make([]byte, 58)),
	}
	head := pipeline.Headers{Date: wireDate, Extra: map[string]string{"X-Example": "yes"}}
	prefix := "Date: " + wireDate + "\r\nX-Example: yes\r\nMIME-Version: 1.0\r\nContent-Type: "
	return []struct {
		name  string
		email *pipeline.EmailJSON
		wire  string
	}{
		{"8-bit leaf", &pipeline.EmailJSON{Headers: head, Body: text},
			prefix + plain + "\r\nContent-Transfer-Encoding: 8bit\r\n\r\nGrüße\r\n"},
		{"empty leaf", &pipeline.EmailJSON{Headers: head},
			prefix + plain + "\r\n\r\n\r\n"},
		{"mixed alternative calendar and attachments", &pipeline.EmailJSON{
			Headers: head, Body: pipeline.Body{Parts: []pipeline.Body{text, calendar}},
			Attachments: []pipeline.Attachment{file, {ContentType: "application/octet-stream"}},
		}, prefix + "multipart/mixed;\r\n boundary=\"b1\"\r\n\r\n" +
			"--b1\r\nContent-Type: multipart/alternative; boundary=\"b2\"\r\n\r\n" +
			"--b2\r\nContent-Transfer-Encoding: 8bit\r\nContent-Type: " + plain + "\r\n\r\nGrüße\r\n" +
			"--b2\r\nContent-Transfer-Encoding: base64\r\nContent-Type: text/calendar; method=REPLY\r\n\r\n" + calendar.Content + "\r\n" +
			"--b2--\r\n\r\n--b1\r\nContent-Disposition: attachment; filename=file.bin\r\n" +
			"Content-Transfer-Encoding: base64\r\nContent-Type: application/octet-stream\r\n\r\n" + strings.Repeat("A", 76) + "\r\nAA==\r\n" +
			"--b1\r\nContent-Disposition: attachment\r\nContent-Transfer-Encoding: base64\r\n" +
			"Content-Type: application/octet-stream\r\n\r\n\r\n--b1--\r\n"},
		{"mixed body flattened with related and inline image", &pipeline.EmailJSON{
			Headers: head, Body: pipeline.Body{ContentType: "multipart/mixed", Parts: []pipeline.Body{
				{ContentType: "multipart/related", Parts: []pipeline.Body{{ContentType: "text/html", Content: "<img src=cid:chart>"}}},
				{},
			}}, Inline: []pipeline.Attachment{image},
		}, prefix + "multipart/mixed;\r\n boundary=\"b1\"\r\n\r\n" +
			"--b1\r\nContent-Type: multipart/related; boundary=\"b2\"\r\n\r\n" +
			"--b2\r\nContent-Type: text/html\r\n\r\n<img src=cid:chart>\r\n--b2--\r\n\r\n" +
			"--b1\r\nContent-Type: " + plain + "\r\n\r\n\r\n" +
			"--b1\r\nContent-Disposition: inline\r\nContent-Id: <chart>\r\nContent-Transfer-Encoding: base64\r\n" +
			"Content-Type: image/png\r\n\r\nAAEC\r\n--b1--\r\n"},
		{"top-level related", &pipeline.EmailJSON{
			Headers: head, Body: pipeline.Body{ContentType: "multipart/related", Parts: []pipeline.Body{text, {}}},
		}, prefix + "multipart/related;\r\n boundary=\"b1\"\r\n\r\n" +
			"--b1\r\nContent-Transfer-Encoding: 8bit\r\nContent-Type: " + plain + "\r\n\r\nGrüße\r\n" +
			"--b1\r\nContent-Type: " + plain + "\r\n\r\n\r\n--b1--\r\n"},
	}
}

func wireBoundary(n int) string { return fmt.Sprintf("%060d", n) }

func expandWire(wire string) string {
	return strings.NewReplacer("b1", wireBoundary(1), "b2", wireBoundary(2)).Replace(wire)
}

// These wire fixtures also run against the serializer before the streaming change.
func TestSerializeLegacyWire(t *testing.T) {
	boundary := regexp.MustCompile(`boundary="([0-9a-f]{60})"`)
	for _, tc := range serializeWireCases() {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := Serialize(tc.email)
			if err != nil {
				t.Fatal(err)
			}
			got := string(raw)
			for i, match := range boundary.FindAllStringSubmatch(got, -1) {
				got = strings.ReplaceAll(got, match[1], wireBoundary(i+1))
			}
			if want := expandWire(tc.wire); got != want {
				t.Fatalf("wire output (%d bytes)\n got: %q\nwant: %q", len(got), got, want)
			}
			t.Logf("matched %d wire bytes", len(got))
		})
	}
}
