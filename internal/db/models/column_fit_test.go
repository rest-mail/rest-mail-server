package models

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestFitFilename(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"short", "a.pdf", "a.pdf"},
		{"exactly the column", strings.Repeat("x", 255), strings.Repeat("x", 255)},
		{"a character over, no extension", strings.Repeat("x", 256), strings.Repeat("x", 255)},
		{"keeps what it opens as", strings.Repeat("x", 300) + ".exe", strings.Repeat("x", 251) + ".exe"},
		{"hides nothing behind spaces", "invoice.pdf" + strings.Repeat(" ", 300) + ".exe", "invoice.pdf" + strings.Repeat(" ", 240) + ".exe"},
		{"an extension too long to be one", strings.Repeat("x", 300) + "." + strings.Repeat("e", 40), strings.Repeat("x", 255)},
		{"counts characters, as the column does", strings.Repeat("日", 255), strings.Repeat("日", 255)},
		{"cuts between characters", strings.Repeat("日", 300) + ".pdf", strings.Repeat("日", 251) + ".pdf"},
		{"a NUL", "a\x00b.pdf", "a�b.pdf"},
		{"invalid UTF-8", "a\xffb.pdf", "a�b.pdf"},
		{"empty", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := FitFilename(tc.in)
			if got != tc.want {
				t.Fatalf("got %q", got)
			}
			if !fitsColumn(got, attachmentFilenameColumn) {
				t.Fatalf("%d characters, valid %v", utf8.RuneCountInString(got), utf8.ValidString(got))
			}
		})
	}
}

func TestAttachmentFitsItsColumns(t *testing.T) {
	longest := "application/" + strings.Repeat("z", 243)
	for _, tc := range []struct{ in, want string }{
		{"application/pdf", "application/pdf"},
		{longest, longest},
		{longest + "z", "application/octet-stream"},
		{"a/b\xff", "application/octet-stream"},
		{"a/b\x00", "application/octet-stream"},
	} {
		attachment := Attachment{Filename: "a.pdf", ContentType: tc.in}
		if err := attachment.BeforeCreate(nil); err != nil {
			t.Fatal(err)
		}
		if attachment.ContentType != tc.want {
			t.Fatalf("%.40q: got %.40q", tc.in, attachment.ContentType)
		}
	}
}

func TestMessageFitsItsColumnsBeforeItsSeal(t *testing.T) {
	longID := "<" + strings.Repeat("i", 2000) + "@x.test>"
	message := Message{
		Sender: strings.Repeat("s", 300) + "\xff", SenderName: strings.Repeat("日", 300),
		MsgID: longID, ThreadID: longID, InReplyTo: longID,
		RawMessage: "From: a@b\r\n\r\nb", RawSize: len("From: a@b\r\n\r\nb"),
	}
	if err := message.BeforeCreate(nil); err != nil {
		t.Fatal(err)
	}
	if message.Sender != strings.Repeat("s", 255) || message.SenderName != strings.Repeat("日", 255) {
		t.Fatalf("sender %q, name %q", message.Sender, message.SenderName)
	}
	if message.InReplyTo != "" {
		t.Fatalf("in-reply-to %d long", len(message.InReplyTo))
	}
	// Both name the one ID, so a message and its replies stay one thread.
	if message.MsgID != message.ThreadID || len(message.MsgID) > messageIDColumn || !strings.HasSuffix(message.MsgID, "@rest-mail.invalid>") {
		t.Fatalf("message id %q, thread %q", message.MsgID, message.ThreadID)
	}
	if other := fitID("<" + strings.Repeat("j", 2000) + "@x.test>"); other == message.MsgID {
		t.Fatalf("two IDs share %q", other)
	}
	if digest, err := message.SealDigest(); err != nil || digest != message.SealedHash {
		t.Fatalf("seal %q, digest %q (%v)", message.SealedHash, digest, err)
	}
	short := Message{Sender: "a@b", MsgID: "<m@x>", ThreadID: "<m@x>", InReplyTo: "<p@x>", SenderName: "A"}
	short.fitColumns()
	if short.Sender != "a@b" || short.MsgID != "<m@x>" || short.ThreadID != "<m@x>" || short.InReplyTo != "<p@x>" || short.SenderName != "A" {
		t.Fatalf("a message that fits changed: %+v", short)
	}
}
