package mail

import (
	"mime"
	stdmail "net/mail"
	"net/url"
	"regexp"
	"strings"
)

var listIdentifier = regexp.MustCompile("^[A-Za-z0-9!#$%&'*+\\-/=?^_`{|}~]+(?:\\.[A-Za-z0-9!#$%&'*+\\-/=?^_`{|}~]+)+$")

func ClassifyHeaders(headers map[string][]string) string {
	if validListID(headerValue(headers, "List-Id")) || validUnsubscribe(headerValue(headers, "List-Unsubscribe")) {
		return "newsletter"
	}
	automation, _, err := mime.ParseMediaType(headerValue(headers, "Auto-Submitted"))
	if err == nil && (automation == "auto-generated" || automation == "auto-replied") {
		return "notification"
	}
	switch strings.ToLower(headerValue(headers, "Precedence")) {
	case "bulk", "list", "junk":
		return "notification"
	}
	return "person"
}

func headerValue(headers map[string][]string, name string) string {
	var values []string
	for key, items := range headers {
		if strings.EqualFold(key, name) {
			values = append(values, items...)
		}
	}
	if len(values) != 1 {
		return ""
	}
	value := strings.ReplaceAll(strings.ReplaceAll(values[0], "\r\n\t", " "), "\r\n ", " ")
	for _, character := range value {
		if character < 32 && character != '\t' || character == 127 {
			return ""
		}
	}
	return strings.TrimSpace(value)
}

func validListID(value string) bool {
	start := strings.IndexByte(value, '<')
	if start < 0 || !strings.HasSuffix(value, ">") || strings.ContainsAny(value[:start], "<>") {
		return false
	}
	identifier := value[start+1 : len(value)-1]
	return len(identifier) <= 255 && listIdentifier.MatchString(identifier)
}

func validUnsubscribe(value string) bool {
	if value == "" {
		return false
	}
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if !strings.HasPrefix(item, "<") || !strings.HasSuffix(item, ">") {
			return false
		}
		address := item[1 : len(item)-1]
		if strings.ContainsAny(address, "<> \t\r\n") {
			return false
		}
		target, err := url.Parse(address)
		if err != nil {
			return false
		}
		switch strings.ToLower(target.Scheme) {
		case "https", "http":
			if target.Hostname() == "" || target.User != nil {
				return false
			}
		case "mailto":
			mailbox, err := stdmail.ParseAddress(target.Opaque)
			if err != nil || mailbox.Address != target.Opaque {
				return false
			}
		default:
			return false
		}
	}
	return true
}
