package imap

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"github.com/restmail/restmail/internal/gateway/apiclient"
)

// parseIMAPCommand parses an IMAP command line into tag, command, and arguments.
// IMAP format: <tag> <command> [<args>]
func parseIMAPCommand(line string) (tag, cmd, args string) {
	parts := strings.SplitN(line, " ", 3)
	if len(parts) < 2 {
		return "", "", ""
	}
	tag = parts[0]
	cmd = parts[1]
	if len(parts) > 2 {
		args = parts[2]
	}
	return
}

// parseIMAPArgs splits IMAP arguments respecting quoted strings and parenthesized lists.
func parseIMAPArgs(args string) []string {
	var result []string
	args = strings.TrimSpace(args)
	i := 0

	for i < len(args) {
		// Skip whitespace
		for i < len(args) && args[i] == ' ' {
			i++
		}
		if i >= len(args) {
			break
		}

		switch args[i] {
		case '"':
			// Quoted string — find closing quote
			end := strings.Index(args[i+1:], `"`)
			if end == -1 {
				result = append(result, args[i:])
				i = len(args)
			} else {
				result = append(result, args[i:i+end+2])
				i = i + end + 2
			}
		case '(':
			// Parenthesized list — find closing paren
			depth := 1
			j := i + 1
			for j < len(args) && depth > 0 {
				if args[j] == '(' {
					depth++
				} else if args[j] == ')' {
					depth--
				}
				j++
			}
			result = append(result, args[i:j])
			i = j
		default:
			// Unquoted token — read until space or special char
			j := i
			for j < len(args) && args[j] != ' ' && args[j] != '(' && args[j] != ')' {
				j++
			}
			result = append(result, args[i:j])
			i = j
		}
	}

	return result
}

// unquote removes surrounding double quotes from a string.
func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}

// decodeBase64 decodes a base64-encoded string.
func decodeBase64(s string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(s)
}

// buildFlags returns IMAP flag string from a message summary.
func buildFlags(msg apiclient.MessageSummary) string {
	var flags []string
	if msg.IsRead {
		flags = append(flags, `\Seen`)
	}
	if msg.IsFlagged {
		flags = append(flags, `\Flagged`)
	}
	if msg.IsStarred {
		flags = append(flags, `\Flagged`) // map starred to flagged
	}
	if msg.IsDraft {
		flags = append(flags, `\Draft`)
	}
	return strings.Join(flags, " ")
}

// buildEnvelope constructs an IMAP ENVELOPE response from a message summary.
func buildEnvelope(msg apiclient.MessageSummary) string {
	date := msg.ReceivedAt.Format("Mon, 02 Jan 2006 15:04:05 -0700")
	subject := quoteString(msg.Subject)
	sender := quoteString(msg.Sender)
	senderName := quoteString(msg.SenderName)

	// Simplified envelope: (date subject from sender reply-to to cc bcc in-reply-to message-id)
	// Each address is ((name NIL user host))
	fromAddr := buildAddress(senderName, sender)

	return fmt.Sprintf("(%s %s %s %s %s NIL NIL NIL NIL NIL)",
		quoteString(date), subject, fromAddr, fromAddr, fromAddr)
}

// buildAddress constructs an IMAP address structure.
func buildAddress(name, email string) string {
	if email == "" {
		return "NIL"
	}
	parts := strings.SplitN(email, "@", 2)
	user := parts[0]
	host := ""
	if len(parts) > 1 {
		host = parts[1]
	}
	return fmt.Sprintf("((%s NIL %s %s))", quoteString(name), quoteString(user), quoteString(host))
}

// quoteString wraps a string in IMAP quotes, or returns NIL for empty.
func quoteString(s string) string {
	if s == "" {
		return "NIL"
	}
	// Escape backslashes and double quotes
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
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
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("\r\n")

	// Body
	if msg.BodyText != "" {
		b.WriteString(msg.BodyText)
	}

	return b.String()
}

// parseSequenceSet parses an IMAP sequence set like "1", "1:5", "1,3,5", "1:*".
func parseSequenceSet(seqStr string, total int) []int {
	if total == 0 {
		return nil
	}

	var result []int
	seen := make(map[int]bool)

	for _, part := range strings.Split(seqStr, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		if strings.Contains(part, ":") {
			// Range
			rangeParts := strings.SplitN(part, ":", 2)
			start := resolveSeqNum(rangeParts[0], total)
			end := resolveSeqNum(rangeParts[1], total)

			if start > end {
				start, end = end, start
			}
			for i := start; i <= end; i++ {
				if i >= 1 && i <= total && !seen[i] {
					result = append(result, i)
					seen[i] = true
				}
			}
		} else {
			// Single number
			n := resolveSeqNum(part, total)
			if n >= 1 && n <= total && !seen[n] {
				result = append(result, n)
				seen[n] = true
			}
		}
	}

	return result
}

// resolveSeqNum resolves a sequence number, handling "*" as the total count.
func resolveSeqNum(s string, total int) int {
	s = strings.TrimSpace(s)
	if s == "*" {
		return total
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}

// parseFlags extracts IMAP flags from a parenthesized list like "(\Seen \Flagged)".
func parseFlags(s string) []string {
	s = strings.TrimSpace(s)
	// Remove surrounding parentheses
	s = strings.TrimPrefix(s, "(")
	s = strings.TrimSuffix(s, ")")

	var flags []string
	for _, f := range strings.Fields(s) {
		if f != "" {
			flags = append(flags, f)
		}
	}
	return flags
}
