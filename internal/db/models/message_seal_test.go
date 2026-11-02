package models

import (
	"strings"
	"testing"
)

func TestMessageSealCoversSourceAndDisplayedContent(t *testing.T) {
	original := Message{Sender: "a@example.test", Subject: "Hello", BodyText: "body", BodyHTML: "<p>body</p>", Headers: JSONB(`{"X-Test":["yes"]}`), RawMessage: "Subject: Hello\r\nX-Test: yes\r\n\r\nbody\r\n"}
	if err := original.BeforeCreate(nil); err != nil {
		t.Fatal(err)
	}
	if len(original.SealedHash) != 64 {
		t.Fatalf("new acceptance has no SHA-256 seal: %q", original.SealedHash)
	}
	for _, field := range []string{"source body", "source header", "display body", "display HTML", "display header", "subject", "sender"} {
		t.Run(field, func(t *testing.T) {
			changed := original
			switch field {
			case "source body":
				changed.RawMessage = strings.Replace(changed.RawMessage, "body", "edited", 1)
			case "source header":
				changed.RawMessage = strings.Replace(changed.RawMessage, "Hello", "edited", 1)
			case "display body":
				changed.BodyText = "edited"
			case "display HTML":
				changed.BodyHTML = "edited"
			case "display header":
				changed.Headers = JSONB(`{"X-Test":["no"]}`)
			case "subject":
				changed.Subject = "edited"
			case "sender":
				changed.Sender = "edited@example.test"
			}
			actual, err := changed.SealDigest()
			if err != nil || actual == original.SealedHash {
				t.Fatalf("edit undetected: digest %q, error %v", actual, err)
			}
		})
	}
	original.Folder, original.IsRead, original.IsFlagged = "Archive", true, true
	actual, err := original.SealDigest()
	if err != nil || actual != original.SealedHash {
		t.Fatalf("flags/move broke seal: %q, %v", actual, err)
	}
}

func TestMessageSealJSONBSurvivesDatabaseFormatting(t *testing.T) {
	message := Message{Sender: "a@example.test", BodyText: "body", Headers: JSONB(`{"Z":["1"],"A":["2"]}`)}
	if err := message.BeforeCreate(nil); err != nil {
		t.Fatal(err)
	}
	message.Headers = JSONB(`{"A": ["2"], "Z": ["1"]}`)
	message.RecipientsTo, message.RecipientsCc = JSONB(`[]`), JSONB(`[]`)
	actual, err := message.SealDigest()
	if err != nil || actual != message.SealedHash {
		t.Fatalf("JSONB formatting broke seal: %q, %v", actual, err)
	}
}

func TestMessageSealDraftAndFolderMarkerAreNotReceipts(t *testing.T) {
	for _, message := range []Message{{PendingDraft: true, IsDraft: true, BodyText: "editable"}, {Sender: "system", IsDeleted: true}} {
		if err := message.BeforeCreate(nil); err != nil || message.SealedHash != "" {
			t.Fatalf("non-receipt sealed: %+v, %v", message, err)
		}
	}
	appended := Message{IsDraft: true, RawMessage: "Subject: draft\r\n\r\nbody"}
	if err := appended.BeforeCreate(nil); err != nil || appended.SealedHash == "" {
		t.Fatalf("APPEND with draft flag was not sealed: %v", err)
	}
}
