package imap

import (
	"fmt"
	"strings"

	"github.com/restmail/restmail/internal/gateway/apiclient"
)

// buildRawMessage constructs a simplified RFC 2822 message from API data.
func buildRawMessage(msg apiclient.MessageDetail) string {
	var b strings.Builder

	// Headers
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
		// Multipart alternative
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

// parseBasicHeaders extracts basic message fields from raw RFC 2822 data, for
// building the structured part of a DeliverRequest on APPEND.
func parseBasicHeaders(data []byte) (subject, bodyText, bodyHTML, messageID, senderName string) {
	raw := string(data)
	headerEnd := strings.Index(raw, "\r\n\r\n")
	if headerEnd < 0 {
		headerEnd = strings.Index(raw, "\n\n")
	}
	if headerEnd < 0 {
		return "", raw, "", "", ""
	}

	headers := raw[:headerEnd]
	body := raw[headerEnd:]
	body = strings.TrimLeft(body, "\r\n")

	for _, line := range strings.Split(headers, "\n") {
		line = strings.TrimRight(line, "\r")
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "subject: ") {
			subject = strings.TrimSpace(line[9:])
		} else if strings.HasPrefix(lower, "message-id: ") {
			messageID = strings.TrimSpace(line[12:])
		} else if strings.HasPrefix(lower, "from: ") {
			fromVal := strings.TrimSpace(line[6:])
			if idx := strings.Index(fromVal, "<"); idx > 0 {
				senderName = strings.TrimSpace(fromVal[:idx])
				senderName = strings.Trim(senderName, "\"")
			}
		}
	}

	bodyText = body
	return
}
