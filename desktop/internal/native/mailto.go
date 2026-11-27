package native

import (
	"errors"
	"net/mail"
	"net/url"
	"strings"
)

// MaxMailtoLength bounds a mailto: link the app accepts. Browsers and the
// operating system pass links of any length, and a link longer than this is
// refused rather than truncated.
const MaxMailtoLength = 64 * 1024

// MaxMailtoRecipients bounds the addresses one link may add across To, Cc and Bcc.
const MaxMailtoRecipients = 100

// Mailto is a new message requested by a mailto: link (RFC 6068).
type Mailto struct {
	To        string `json:"to"`
	Cc        string `json:"cc"`
	Bcc       string `json:"bcc"`
	Subject   string `json:"subject"`
	Body      string `json:"body"`
	InReplyTo string `json:"inReplyTo"`
}

var errNotMailto = errors.New("not a mailto: link")

// MailtoArgument returns the first command-line argument that is a mailto:
// link, or "" when there is none. Windows and Linux pass the link as an
// argument when the app is the default mail handler.
func MailtoArgument(args []string) string {
	for _, argument := range args {
		if IsMailto(argument) {
			return argument
		}
	}
	return ""
}

// IsMailto reports whether raw uses the mailto scheme, ignoring case.
func IsMailto(raw string) bool {
	return len(raw) >= len("mailto:") && strings.EqualFold(raw[:len("mailto:")], "mailto:")
}

// ParseMailto reads a mailto: link into the fields of a new message.
//
// It follows RFC 6068: addresses in the path and in to, cc and bcc header
// fields are comma separated, every part is percent-decoded, and "+" is a
// literal plus, not a space. Unknown header fields are ignored, as the RFC
// asks, so a link cannot set arbitrary headers. Every address must parse,
// and header values may not contain line breaks.
func ParseMailto(raw string) (Mailto, error) {
	if len(raw) > MaxMailtoLength {
		return Mailto{}, errors.New("mailto: link is too long")
	}
	if !IsMailto(raw) {
		return Mailto{}, errNotMailto
	}
	rest := raw[len("mailto:"):]
	if index := strings.IndexByte(rest, '#'); index >= 0 {
		rest = rest[:index]
	}
	path, query, _ := strings.Cut(rest, "?")
	var to, cc, bcc []string
	var result Mailto
	count := 0
	add := func(list *[]string, encoded string) error {
		value, err := url.PathUnescape(encoded)
		if err != nil {
			return errors.New("mailto: link has invalid percent-encoding")
		}
		for part := range strings.SplitSeq(value, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			if err := checkHeaderValue(part); err != nil {
				return err
			}
			if _, err := mail.ParseAddress(part); err != nil {
				return errors.New("mailto: link has an invalid address")
			}
			count++
			if count > MaxMailtoRecipients {
				return errors.New("mailto: link has too many recipients")
			}
			*list = append(*list, part)
		}
		return nil
	}
	if err := add(&to, path); err != nil {
		return Mailto{}, err
	}
	if query != "" {
		for field := range strings.SplitSeq(query, "&") {
			if field == "" {
				continue
			}
			name, encoded, _ := strings.Cut(field, "=")
			name, err := url.PathUnescape(name)
			if err != nil {
				return Mailto{}, errors.New("mailto: link has invalid percent-encoding")
			}
			switch strings.ToLower(name) {
			case "to":
				err = add(&to, encoded)
			case "cc":
				err = add(&cc, encoded)
			case "bcc":
				err = add(&bcc, encoded)
			case "subject":
				result.Subject, err = headerValue(encoded)
			case "in-reply-to":
				result.InReplyTo, err = headerValue(encoded)
			case "body":
				result.Body, err = url.PathUnescape(encoded)
				if err != nil {
					err = errors.New("mailto: link has invalid percent-encoding")
				}
				result.Body = strings.ReplaceAll(result.Body, "\r\n", "\n")
			}
			if err != nil {
				return Mailto{}, err
			}
		}
	}
	result.To = strings.Join(to, ", ")
	result.Cc = strings.Join(cc, ", ")
	result.Bcc = strings.Join(bcc, ", ")
	return result, nil
}

func headerValue(encoded string) (string, error) {
	value, err := url.PathUnescape(encoded)
	if err != nil {
		return "", errors.New("mailto: link has invalid percent-encoding")
	}
	if err := checkHeaderValue(value); err != nil {
		return "", err
	}
	return strings.TrimSpace(value), nil
}

func checkHeaderValue(value string) error {
	if strings.ContainsAny(value, "\r\n\x00") {
		return errors.New("mailto: link has a line break in a header field")
	}
	return nil
}
