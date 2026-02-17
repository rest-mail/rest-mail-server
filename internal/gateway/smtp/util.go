package smtp

import (
	"encoding/base64"
	"mime"
	"mime/multipart"
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

// parseRawMessage parses an RFC 2822 message to extract key headers and body parts.
func parseRawMessage(data []byte) (subject, bodyText, bodyHTML, messageID, senderName string) {
	msg := string(data)

	// Split headers and body at the first blank line
	headerEnd := strings.Index(msg, "\r\n\r\n")
	if headerEnd == -1 {
		headerEnd = strings.Index(msg, "\n\n")
	}

	var headers, body string
	var contentType string
	if headerEnd >= 0 {
		headers = msg[:headerEnd]
		body = msg[headerEnd:]
		body = strings.TrimLeft(body, "\r\n")
	} else {
		headers = msg
	}

	// Parse headers (handle continuation lines)
	var unfoldedHeaders []string
	for _, line := range strings.Split(headers, "\n") {
		line = strings.TrimRight(line, "\r")
		if len(line) > 0 && (line[0] == ' ' || line[0] == '\t') {
			// Continuation line
			if len(unfoldedHeaders) > 0 {
				unfoldedHeaders[len(unfoldedHeaders)-1] += " " + strings.TrimSpace(line)
			}
		} else {
			unfoldedHeaders = append(unfoldedHeaders, line)
		}
	}

	for _, line := range unfoldedHeaders {
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "subject:") {
			subject = strings.TrimSpace(line[8:])
		} else if strings.HasPrefix(lower, "message-id:") {
			messageID = strings.TrimSpace(line[11:])
			messageID = strings.Trim(messageID, "<>")
		} else if strings.HasPrefix(lower, "from:") {
			from := strings.TrimSpace(line[5:])
			if idx := strings.Index(from, "<"); idx > 0 {
				senderName = strings.TrimSpace(from[:idx])
				senderName = strings.Trim(senderName, "\"")
			}
		} else if strings.HasPrefix(lower, "content-type:") {
			contentType = strings.TrimSpace(line[13:])
		}
	}

	// Parse body based on content type
	if contentType != "" && strings.Contains(strings.ToLower(contentType), "multipart/") {
		bodyText, bodyHTML = parseMultipartBody(contentType, body)
	} else if strings.Contains(strings.ToLower(contentType), "text/html") {
		bodyHTML = body
	} else {
		bodyText = body
	}

	return
}

// parseMultipartBody extracts text/plain and text/html parts from a multipart body.
func parseMultipartBody(contentType, body string) (text, html string) {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return body, ""
	}

	boundary := params["boundary"]
	if boundary == "" {
		return body, ""
	}

	reader := multipart.NewReader(strings.NewReader(body), boundary)
	for {
		part, err := reader.NextPart()
		if err != nil {
			break
		}
		partType := part.Header.Get("Content-Type")
		partData := readPart(part)

		lowerType := strings.ToLower(partType)
		if strings.HasPrefix(lowerType, "text/plain") && text == "" {
			text = partData
		} else if strings.HasPrefix(lowerType, "text/html") && html == "" {
			html = partData
		} else if strings.HasPrefix(lowerType, "multipart/") {
			// Nested multipart (e.g., multipart/alternative inside multipart/mixed)
			nestedText, nestedHTML := parseMultipartBody(partType, partData)
			if text == "" {
				text = nestedText
			}
			if html == "" {
				html = nestedHTML
			}
		}
	}

	_ = mediaType // used for validation above
	return
}

// readPart reads all data from a multipart part, handling Content-Transfer-Encoding.
func readPart(part *multipart.Part) string {
	var buf strings.Builder
	data := make([]byte, 4096)
	for {
		n, err := part.Read(data)
		if n > 0 {
			buf.Write(data[:n])
		}
		if err != nil {
			break
		}
	}

	raw := buf.String()
	encoding := strings.ToLower(part.Header.Get("Content-Transfer-Encoding"))
	switch encoding {
	case "base64":
		decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(raw, "\n", ""))
		if err == nil {
			return string(decoded)
		}
	case "quoted-printable":
		return decodeQuotedPrintable(raw)
	}
	return raw
}

// decodeQuotedPrintable decodes quoted-printable encoded text.
func decodeQuotedPrintable(s string) string {
	var result strings.Builder
	lines := strings.Split(s, "\n")
	for _, line := range lines {
		line = strings.TrimRight(line, "\r")
		// Soft line break
		if strings.HasSuffix(line, "=") {
			line = line[:len(line)-1]
			result.WriteString(decodeQPLine(line))
		} else {
			result.WriteString(decodeQPLine(line))
			result.WriteString("\n")
		}
	}
	return strings.TrimRight(result.String(), "\n")
}

func decodeQPLine(line string) string {
	var result strings.Builder
	i := 0
	for i < len(line) {
		if line[i] == '=' && i+2 < len(line) {
			hi := unhex(line[i+1])
			lo := unhex(line[i+2])
			if hi >= 0 && lo >= 0 {
				result.WriteByte(byte(hi<<4 | lo))
				i += 3
				continue
			}
		}
		result.WriteByte(line[i])
		i++
	}
	return result.String()
}

func unhex(c byte) int {
	switch {
	case '0' <= c && c <= '9':
		return int(c - '0')
	case 'A' <= c && c <= 'F':
		return int(c - 'A' + 10)
	case 'a' <= c && c <= 'f':
		return int(c - 'a' + 10)
	}
	return -1
}

