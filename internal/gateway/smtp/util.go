package smtp

import (
	"encoding/base64"
	"strings"
)

// parseCommand splits an SMTP command line into command and argument.
func parseCommand(line string) (string, string) {
	parts := strings.SplitN(line, " ", 2)
	cmd := strings.ToUpper(parts[0])
	arg := ""
	if len(parts) > 1 {
		arg = parts[1]
	}
	return cmd, arg
}

// extractAddress extracts the email address from MAIL FROM:<addr> or RCPT TO:<addr>.
func extractAddress(arg, prefix string) string {
	upper := strings.ToUpper(arg)
	prefixStr := strings.ToUpper(prefix) + ":"
	idx := strings.Index(upper, prefixStr)
	if idx == -1 {
		return ""
	}

	rest := arg[idx+len(prefixStr):]
	rest = strings.TrimSpace(rest)

	// Extract from angle brackets
	if strings.HasPrefix(rest, "<") {
		end := strings.Index(rest, ">")
		if end == -1 {
			return ""
		}
		return rest[1:end]
	}

	// No brackets: take up to the first space or end of string
	parts := strings.Fields(rest)
	if len(parts) == 0 {
		return ""
	}
	return parts[0]
}

// extractIP extracts the IP address from a remote address string (host:port).
func extractIP(addr string) string {
	host, _, err := splitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}

// splitHostPort is a simple host:port splitter.
func splitHostPort(addr string) (string, string, error) {
	// Handle IPv6
	if strings.HasPrefix(addr, "[") {
		end := strings.Index(addr, "]")
		if end == -1 {
			return addr, "", nil
		}
		host := addr[1:end]
		if end+1 < len(addr) && addr[end+1] == ':' {
			return host, addr[end+2:], nil
		}
		return host, "", nil
	}

	last := strings.LastIndex(addr, ":")
	if last == -1 {
		return addr, "", nil
	}
	return addr[:last], addr[last+1:], nil
}

// decodeBase64 decodes a base64-encoded string.
func decodeBase64(s string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(s)
}

// parseRawMessage does a minimal parse of an RFC 2822 message to extract key headers and body.
func parseRawMessage(data []byte) (subject, bodyText, bodyHTML, messageID, senderName string) {
	msg := string(data)

	// Split headers and body at the first blank line
	headerEnd := strings.Index(msg, "\r\n\r\n")
	if headerEnd == -1 {
		headerEnd = strings.Index(msg, "\n\n")
	}

	var headers, body string
	if headerEnd >= 0 {
		headers = msg[:headerEnd]
		body = msg[headerEnd:]
		// Trim leading blank lines from body
		body = strings.TrimLeft(body, "\r\n")
	} else {
		headers = msg
	}

	// Parse headers (simple single-line extraction)
	for _, line := range strings.Split(headers, "\n") {
		line = strings.TrimRight(line, "\r")
		lower := strings.ToLower(line)

		if strings.HasPrefix(lower, "subject:") {
			subject = strings.TrimSpace(line[8:])
		} else if strings.HasPrefix(lower, "message-id:") {
			messageID = strings.TrimSpace(line[11:])
			messageID = strings.Trim(messageID, "<>")
		} else if strings.HasPrefix(lower, "from:") {
			from := strings.TrimSpace(line[5:])
			// Extract display name: "Alice Smith" <alice@mail.test>
			if idx := strings.Index(from, "<"); idx > 0 {
				senderName = strings.TrimSpace(from[:idx])
				senderName = strings.Trim(senderName, "\"")
			}
		}
	}

	// For now, treat the entire body as plain text
	// TODO: Proper MIME multipart parsing
	bodyText = body

	return
}
