package mime

import (
	"mime/quotedprintable"
	"strings"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/pipeline"
)

// Generated is a message this server writes itself and files straight into a mailbox: a
// quarantine digest, a sending-limit notice, a test message. Its text may quote what a stranger
// sent, so the renderer accepts any length and any line endings.
type Generated struct {
	From      pipeline.Address
	To        []pipeline.Address
	Subject   string
	BodyText  string
	BodyHTML  string // optional: with it the message is multipart/alternative
	MessageID string // with its angle brackets, as rmail.GenerateMessageID returns it
	Date      time.Time
}

// RenderGenerated renders the message that is stored as its raw source, so the gateways serve
// and measure it as they do any other stored mail. Bodies go out quoted-printable: every line
// ends in CRLF and stays under the 998 octets RFC 5322 section 2.1.1 allows, whatever the text
// holds, and the message is 7-bit, so no gateway has to convert it on the way out.
func RenderGenerated(m Generated) (string, error) {
	text, err := quotedPrintable(m.BodyText)
	if err != nil {
		return "", err
	}
	body := pipeline.Body{ContentType: "text/plain; charset=utf-8", Content: text, TransferEncoding: "quoted-printable"}
	if m.BodyHTML != "" {
		html, err := quotedPrintable(m.BodyHTML)
		if err != nil {
			return "", err
		}
		body = pipeline.Body{ContentType: "multipart/alternative", Parts: []pipeline.Body{
			body,
			{ContentType: "text/html; charset=utf-8", Content: html, TransferEncoding: "quoted-printable"},
		}}
	}
	raw, err := Serialize(&pipeline.EmailJSON{
		Headers: pipeline.Headers{
			From:      []pipeline.Address{m.From},
			To:        m.To,
			Subject:   m.Subject,
			Date:      m.Date.Format(time.RFC1123Z),
			MessageID: m.MessageID,
			// RFC 3834 section 5: nothing should auto-reply to mail the server wrote.
			Extra: map[string]string{"Auto-Submitted": "auto-generated"},
		},
		Body: body,
	})
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// quotedPrintable encodes text, with each of its line breaks as a CRLF.
func quotedPrintable(text string) (string, error) {
	var b strings.Builder
	w := quotedprintable.NewWriter(&b)
	if _, err := w.Write([]byte(text)); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	return b.String(), nil
}
