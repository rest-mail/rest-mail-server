package imap

import (
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/restmail/restmail/internal/gateway/apiclient"
)

// maxFolderNameLen bounds an IMAP folder (mailbox) name. The messages.folder
// column is size:255, so a longer name can never match a stored row anyway;
// rejecting it at the gateway keeps oversized, attacker-chosen names out of the
// downstream API request entirely.
const maxFolderNameLen = 255

// validateFolder is the gateway's object-level authorization guard for
// folder-scoped IMAP operations (OSI-9). Every folder name a client supplies —
// on SELECT/STATUS (Messages), COPY/MOVE and APPEND destinations — passes
// through here before it is used to build an API request path or JSON body.
//
// The backend already scopes each folder query to the authenticated account's
// mailbox (resolveAccountMailbox), so this is defense in depth: it rejects
// names that could smuggle control characters or CR/LF into the downstream
// request (header/JSON injection), traverse outside the intended path, or blow
// past the storage column width. It mirrors the control-character/newline
// rejection already applied to operator-supplied identifiers elsewhere in the
// codebase (e.g. the dnsmasq record writer).
//
// The check is an allow-by-shape rule: a non-empty name, no longer than
// maxFolderNameLen bytes, valid UTF-8, with no control characters (which
// includes NUL, TAB, CR and LF) and no NUL or path-traversal ("..") sequence.
// Ordinary hierarchical names ("INBOX", "Sent", "Work/Projects",
// "[Gmail]/All Mail") are accepted; only structurally dangerous names are
// refused.
func validateFolder(folder string) error {
	if folder == "" {
		return fmt.Errorf("imap: empty folder name")
	}
	if len(folder) > maxFolderNameLen {
		return fmt.Errorf("imap: folder name too long (%d > %d bytes)", len(folder), maxFolderNameLen)
	}
	if !utf8.ValidString(folder) {
		return fmt.Errorf("imap: folder name is not valid UTF-8")
	}
	for _, r := range folder {
		// Reject C0/C1 control characters and DEL. This covers NUL, TAB, CR and
		// LF, the characters usable to inject into the downstream HTTP request
		// line or JSON body.
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return fmt.Errorf("imap: folder name contains a control character")
		}
	}
	if strings.Contains(folder, "..") {
		return fmt.Errorf("imap: folder name contains a path-traversal sequence")
	}
	return nil
}

// toUID converts a rest-mail message ID to an IMAP UID. rest-mail's message ID is
// the message's IMAP UID (a global message-ID-as-UID model). The ID is a uint,
// which may be 64-bit, so clamp values that do not fit in the 32-bit UID space to
// 0 rather than letting them silently wrap to a small, wrong UID. A 0 result is
// not a valid UID: the APPENDUID/COPYUID callers treat it as a failure and return
// an error, so the client never receives an APPENDUID/COPYUID response code
// naming UID 0 (go-imap emits the resp-code even for a 0 UID).
func toUID(id uint) uint32 {
	if uint64(id) > math.MaxUint32 {
		return 0
	}
	return uint32(id)
}

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
