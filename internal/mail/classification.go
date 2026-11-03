package mail

import (
	"bufio"
	"io"
	"mime"
	stdmail "net/mail"
	"net/textproto"
	"net/url"
	"regexp"
	"strings"
)

// The classes of a message. Only ClassNewsletter is shaped like a list, so it is the only
// one a client may collapse.
const (
	ClassNewsletter   = "newsletter"
	ClassReceipt      = "receipt"
	ClassNotification = "notification"
	ClassPerson       = "person"
)

// SieveReceiptKeyword is the imap4flags keyword (RFC 5232) with which a Sieve rule says a
// message is a receipt. No header says so, which leaves a rule as the only source of that class.
const SieveReceiptKeyword = "$receipt"

// foldedLineBreak is a line break followed by whitespace (RFC 5322 section 2.2.3). Any other
// line break left in a value makes the value unusable, because it could inject a field.
var foldedLineBreak = regexp.MustCompile(`\r?\n[ \t]+`)

var listIdentifier = regexp.MustCompile("^[A-Za-z0-9!#$%&'*+\\-/=?^_`{|}~]+(?:\\.[A-Za-z0-9!#$%&'*+\\-/=?^_`{|}~]+)+$")

// Classify is the class of a message, from its headers and the class a rule recorded for it
// at delivery. The order is: a rule's receipt, list headers, automation headers, person. A
// rule wins because its owner chose it and because a receipt never collapses, so the rule
// can only keep mail visible. No other value from a rule changes the answer, which means
// that no rule can make a message collapse.
func Classify(headers map[string][]string, ruleClass string) string {
	if ruleClass == ClassReceipt {
		return ClassReceipt
	}
	return ClassifyHeaders(headers)
}

// RuleClassOf is the class that the imap4flags keywords a Sieve rule left on a message
// assign, "" when they assign none. IMAP keywords are case-insensitive.
func RuleClassOf(keywords []string) string {
	for _, keyword := range keywords {
		if strings.EqualFold(keyword, SieveReceiptKeyword) {
			return ClassReceipt
		}
	}
	return ""
}

// SourceHeaders reads the unfolded header fields of an RFC 5322 source, stopping at the
// first blank line and never reading the body. A malformed field ends the read: what came
// before it still counts, and a signal after it is not seen, so the message stays visible.
func SourceHeaders(source io.Reader) map[string][]string {
	// The error is that malformed field, and the fields before it are returned with it.
	fields, _ := textproto.NewReader(bufio.NewReader(source)).ReadMIMEHeader()
	return fields
}

func ClassifyHeaders(headers map[string][]string) string {
	if validListID(headerValue(headers, "List-Id")) || validUnsubscribe(headerValue(headers, "List-Unsubscribe")) {
		return ClassNewsletter
	}
	automation, _, err := mime.ParseMediaType(headerValue(headers, "Auto-Submitted"))
	if err == nil && (automation == "auto-generated" || automation == "auto-replied") {
		return ClassNotification
	}
	switch strings.ToLower(headerValue(headers, "Precedence")) {
	case "bulk", "list", "junk":
		return ClassNotification
	}
	return ClassPerson
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
	value := foldedLineBreak.ReplaceAllString(values[0], " ")
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
