package handlers

import (
	"strings"
	"testing"

	"github.com/restmail/restmail/internal/db/models"
)

// Forwarding built the note and the "Forwarded message" header into the text
// body only, and passed the original's HTML through untouched. A client that
// shows the HTML part — which is most of them — then showed the forwarded
// content with no note and no sign that it had been forwarded (issue #286).
//
// Both bodies must carry the same note and header, and the note is somebody's
// typing, so it is escaped rather than dropped into the markup.
func TestForwardBodies(t *testing.T) {
	original := models.Message{
		Sender:   "alice@example.test",
		Subject:  "Quarterly figures",
		BodyText: "The numbers are attached.",
		BodyHTML: "<p>The numbers are <b>attached</b>.</p>",
	}

	t.Run("the HTML part carries the note and the header", func(t *testing.T) {
		_, html := forwardBodies("See below", &original)
		if html == "" {
			t.Fatal("no HTML body was built")
		}
		if !strings.Contains(html, "See below") {
			t.Error("the note is missing from the HTML part")
		}
		if !strings.Contains(html, "Forwarded message") {
			t.Error("the forwarded-message header is missing from the HTML part")
		}
		if !strings.Contains(html, "alice@example.test") || !strings.Contains(html, "Quarterly figures") {
			t.Error("the original's sender and subject are missing from the HTML part")
		}
		if !strings.Contains(html, "<b>attached</b>") {
			t.Error("the original HTML is missing from the forwarded HTML part")
		}
		if strings.Index(html, "See below") > strings.Index(html, "<b>attached</b>") {
			t.Error("the note appears below the original rather than above it")
		}
	})

	t.Run("the note is escaped", func(t *testing.T) {
		_, html := forwardBodies(`<script>alert("x")</script> & co`, &original)
		if strings.Contains(html, "<script>") {
			t.Error("the note was put into the markup unescaped")
		}
		if !strings.Contains(html, "&lt;script&gt;") {
			t.Error("the note's markup was dropped instead of escaped")
		}
		if !strings.Contains(html, "&amp; co") {
			t.Error("the ampersand in the note was not escaped")
		}
	})

	t.Run("the original's sender and subject are escaped too", func(t *testing.T) {
		hostile := models.Message{
			Sender:   `"<img src=x onerror=alert(1)>"@example.test`,
			Subject:  "5 < 6 & 7 > 6",
			BodyText: "hello",
			BodyHTML: "<p>hello</p>",
		}
		_, html := forwardBodies("note", &hostile)
		if strings.Contains(html, "<img src=x") {
			t.Error("the original's sender was put into the markup unescaped")
		}
		if !strings.Contains(html, "5 &lt; 6 &amp; 7 &gt; 6") {
			t.Error("the original's subject was not escaped")
		}
	})

	t.Run("the text part is unchanged in shape", func(t *testing.T) {
		text, _ := forwardBodies("See below", &original)
		for _, want := range []string{
			"See below",
			"---------- Forwarded message ----------",
			"From: alice@example.test",
			"Subject: Quarterly figures",
			"The numbers are attached.",
		} {
			if !strings.Contains(text, want) {
				t.Errorf("the text part lost %q", want)
			}
		}
	})

	t.Run("an empty note leaves no stray blank lines", func(t *testing.T) {
		text, html := forwardBodies("", &original)
		if strings.HasPrefix(text, "\n") {
			t.Error("the text part starts with a blank line when there is no note")
		}
		if !strings.Contains(html, "Forwarded message") {
			t.Error("the header is missing when there is no note")
		}
	})

	t.Run("a text-only original stays text-only", func(t *testing.T) {
		textOnly := models.Message{
			Sender:   "alice@example.test",
			Subject:  "Plain",
			BodyText: "Just text.",
		}
		_, html := forwardBodies("See below", &textOnly)
		if html != "" {
			t.Errorf("an HTML part was invented for a text-only original: %q", html)
		}
	})
}
