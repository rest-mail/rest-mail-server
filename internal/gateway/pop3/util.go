package pop3

import (
	"fmt"
	"strings"

	"github.com/restmail/restmail/internal/gateway/apiclient"
)

// parseCommand splits a POP3 command line into command and argument.
func parseCommand(line string) (string, string) {
	parts := strings.SplitN(line, " ", 2)
	cmd := strings.ToUpper(parts[0])
	arg := ""
	if len(parts) > 1 {
		arg = parts[1]
	}
	return cmd, arg
}

// buildRawMessage constructs a simplified RFC 2822 message from API data.
func buildRawMessage(msg apiclient.MessageDetail) string {
	var b strings.Builder

	b.WriteString(fmt.Sprintf("From: %s <%s>\r\n", msg.SenderName, msg.Sender))
	b.WriteString(fmt.Sprintf("Subject: %s\r\n", msg.Subject))
	b.WriteString(fmt.Sprintf("Date: %s\r\n", msg.ReceivedAt.Format("Mon, 02 Jan 2006 15:04:05 -0700")))
	if msg.MessageID != "" {
		b.WriteString(fmt.Sprintf("Message-ID: <%s>\r\n", msg.MessageID))
	}
	if msg.InReplyTo != "" {
		b.WriteString(fmt.Sprintf("In-Reply-To: <%s>\r\n", msg.InReplyTo))
	}
	b.WriteString("MIME-Version: 1.0\r\n")

	if msg.BodyText != "" && msg.BodyHTML != "" {
		boundary := fmt.Sprintf("=_restmail_%d", msg.ReceivedAt.UnixNano())
		b.WriteString(fmt.Sprintf("Content-Type: multipart/alternative; boundary=\"%s\"\r\n", boundary))
		b.WriteString("\r\n")
		b.WriteString("--" + boundary + "\r\n")
		b.WriteString("Content-Type: text/plain; charset=utf-8\r\n\r\n")
		b.WriteString(msg.BodyText + "\r\n")
		b.WriteString("--" + boundary + "\r\n")
		b.WriteString("Content-Type: text/html; charset=utf-8\r\n\r\n")
		b.WriteString(msg.BodyHTML + "\r\n")
		b.WriteString("--" + boundary + "--\r\n")
	} else if msg.BodyHTML != "" {
		b.WriteString("Content-Type: text/html; charset=utf-8\r\n")
		b.WriteString("\r\n")
		b.WriteString(msg.BodyHTML)
	} else {
		b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
		b.WriteString("\r\n")
		b.WriteString(msg.BodyText)
	}

	return b.String()
}
