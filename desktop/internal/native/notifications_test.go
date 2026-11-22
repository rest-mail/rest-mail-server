package native

import (
	"strings"
	"testing"
)

func TestNewMailNotificationCountsAndTargets(t *testing.T) {
	if got := NewMailNotification(nil); got != (MailNotification{}) {
		t.Fatalf("empty notification: %+v", got)
	}
	if got := NewMailNotification([]MailNotice{{ID: 42}}); got != (MailNotification{MessageID: 42, Title: "Unknown sender", Body: "(No subject)"}) {
		t.Fatalf("single notification: %+v", got)
	}
	if got := NewMailNotification([]MailNotice{{ID: 42, Sender: "Alice", Subject: "Hello"}}); got != (MailNotification{MessageID: 42, Title: "Alice", Body: "Hello"}) {
		t.Fatalf("single metadata: %+v", got)
	}
	messages := []MailNotice{{ID: 1, Sender: "Alice"}, {ID: 2, Sender: "Bob"}, {ID: 3, Sender: "Alice"}, {ID: 4, Sender: "Carol"}, {ID: 5, Sender: "Dave", Subject: "Not in summary"}}
	got := NewMailNotification(messages)
	if got.MessageID != 0 || got.Title != "5 new messages" || got.Body != "From Alice, Bob, Carol" || strings.Contains(got.Body, "Not in summary") {
		t.Fatalf("batch notification: %+v", got)
	}
	if got := NewMailNotification([]MailNotice{{ID: 1}, {ID: 2}}); got.Body != "From Unknown sender" {
		t.Fatalf("unknown senders: %+v", got)
	}
}

func TestUnreadNotification(t *testing.T) {
	for _, test := range []struct {
		count int
		title string
	}{{-1, ""}, {0, ""}, {1, "You have 1 unread message"}, {3, "You have 3 unread messages"}} {
		got := UnreadNotification(test.count)
		if got.Title != test.title || got.Body != "" || got.MessageID != 0 {
			t.Errorf("unread %d: %+v", test.count, got)
		}
	}
}
