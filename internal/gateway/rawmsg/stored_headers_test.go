package rawmsg

import (
	"mime"
	"net/mail"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/gateway/apiclient"
)

func TestBuildStoredHeaderLineBreaks(t *testing.T) {
	for _, field := range []struct {
		name string
		set  func(*apiclient.MessageDetail, string)
		get  func(*mail.Message) string
	}{
		{"Message-ID", func(msg *apiclient.MessageDetail, value string) { msg.MsgID = value },
			func(msg *mail.Message) string { return msg.Header.Get("Message-ID") }},
		{"In-Reply-To", func(msg *apiclient.MessageDetail, value string) { msg.InReplyTo = value },
			func(msg *mail.Message) string { return msg.Header.Get("In-Reply-To") }},
		{"Subject", func(msg *apiclient.MessageDetail, value string) { msg.Subject = value },
			func(msg *mail.Message) string { return msg.Header.Get("Subject") }},
		{"Sender", func(msg *apiclient.MessageDetail, value string) { msg.Sender = value },
			func(msg *mail.Message) string {
				address, err := mail.ParseAddress(msg.Header.Get("From"))
				if err != nil {
					return ""
				}
				return address.Address
			}},
		{"SenderName", func(msg *apiclient.MessageDetail, value string) { msg.SenderName = value },
			func(msg *mail.Message) string {
				address, err := mail.ParseAddress(msg.Header.Get("From"))
				if err != nil {
					return ""
				}
				return address.Name
			}},
	} {
		for _, separator := range []string{"\r", "\n", "\r\n", "\r\n\t", "\r\n\r\n"} {
			t.Run(field.name+"/"+strings.NewReplacer("\r", "CR", "\n", "LF", "\t", "TAB").Replace(separator), func(t *testing.T) {
				msg := testMessage()
				value := "before" + separator + "X-Stored-Header: after@example.test"
				field.set(&msg, value)
				raw := Build(msg)
				header, body := split(t, raw)
				if body != msg.BodyText {
					t.Errorf("body = %q, want %q", body, msg.BodyText)
				}
				for _, line := range strings.Split(header, "\r\n") {
					if strings.ContainsAny(line, "\r\n") || strings.HasPrefix(line, "X-Stored-Header:") {
						t.Errorf("stored value escaped its header: %q", line)
					}
				}
				parsed, err := mail.ReadMessage(strings.NewReader(raw))
				if err != nil {
					t.Fatal(err)
				}
				got, err := new(mime.WordDecoder).DecodeHeader(field.get(parsed))
				if err != nil {
					t.Fatal(err)
				}
				want := strings.NewReplacer("\r", "", "\n", "").Replace(value)
				if !strings.Contains(got, want) {
					t.Errorf("header = %q, want preserved text %q", got, want)
				}
			})
		}
	}
}

func TestBuildStoredEmptyIDsAndReferences(t *testing.T) {
	msg := testMessage()
	msg.MsgID = "\r\n"
	msg.InReplyTo = "\n\r"
	msg.References = "reference@example.test\r\nX-Stored-Header: reference"
	header, body := split(t, Build(msg))
	for _, name := range []string{"Message-ID:", "In-Reply-To:", "References:", "X-Stored-Header:"} {
		if strings.Contains(header, name) {
			t.Errorf("unexpected header %q in %q", name, header)
		}
	}
	if body != msg.BodyText {
		t.Errorf("body = %q, want %q", body, msg.BodyText)
	}
}
