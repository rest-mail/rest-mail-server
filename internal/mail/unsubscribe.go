package mail

import (
	stdmail "net/mail"
	"net/url"
	"strings"
)

// UnsubscribeOneClick is the one value RFC 8058 §3.1 lets List-Unsubscribe-Post carry, and
// the body the one-click POST sends.
const UnsubscribeOneClick = "List-Unsubscribe=One-Click"

// maxUnsubscribeTargetLen bounds one target, so one header cannot hand the API an
// unbounded URL.
const maxUnsubscribeTargetLen = 2048

// UnsubscribeMailto is a mailto: target of List-Unsubscribe (RFC 2369 §3.2): the address
// and the subject and body its URI names (RFC 6068 §2).
type UnsubscribeMailto struct {
	To      string
	Subject string
	Body    string
}

// UnsubscribeTargets are the ways a message's List-Unsubscribe field offers to stop its
// mail. OneClick is set only when List-Unsubscribe-Post says List-Unsubscribe=One-Click
// (RFC 8058 §3.1), and is then the first HTTPS URI of the field; Links are the field's other
// HTTPS URIs, which a person opens in a browser. The field is written by the sender, so
// every target here has been checked: an item that does not parse is left out.
type UnsubscribeTargets struct {
	OneClick string
	Links    []string
	Mailto   []UnsubscribeMailto
}

// Empty reports whether the message offers no target.
func (t UnsubscribeTargets) Empty() bool {
	return t.OneClick == "" && len(t.Links) == 0 && len(t.Mailto) == 0
}

// ParseUnsubscribe reads the unsubscribe targets of headers, as SourceHeaders or the stored
// decoded headers give them. A field given more than once, or carrying a control character,
// is not read, as ClassifyHeaders does not read it.
func ParseUnsubscribe(headers map[string][]string) UnsubscribeTargets {
	var targets UnsubscribeTargets
	value := headerValue(headers, "List-Unsubscribe")
	if value == "" {
		return targets
	}
	oneClick := headerValue(headers, "List-Unsubscribe-Post") == UnsubscribeOneClick
	for _, item := range unsubscribeItems(value) {
		if len(item) > maxUnsubscribeTargetLen {
			continue
		}
		target, err := url.Parse(item)
		if err != nil {
			continue
		}
		switch strings.ToLower(target.Scheme) {
		case "https":
			if !UnsubscribeHTTPSValid(target) {
				continue
			}
			if oneClick && targets.OneClick == "" {
				targets.OneClick = target.String()
				continue
			}
			targets.Links = append(targets.Links, target.String())
		case "mailto":
			if m, ok := parseUnsubscribeMailto(target); ok {
				targets.Mailto = append(targets.Mailto, m)
			}
		}
	}
	return targets
}

// UnsubscribeHTTPSValid reports whether target is an HTTPS URL the server may POST to: a
// host name and no user information, since RFC 8058 §3.1 sends no credentials.
func UnsubscribeHTTPSValid(target *url.URL) bool {
	return strings.EqualFold(target.Scheme, "https") && target.Hostname() != "" && target.User == nil &&
		target.Opaque == "" && target.Fragment == ""
}

// unsubscribeItems splits a List-Unsubscribe value into the URIs its angle brackets hold
// (RFC 2369 §2). Whitespace inside the brackets is ignored, as RFC 2369 lets a long URI
// fold; a comment or text between items is skipped.
func unsubscribeItems(value string) []string {
	var items []string
	for {
		start := strings.IndexByte(value, '<')
		if start < 0 {
			return items
		}
		end := strings.IndexByte(value[start:], '>')
		if end < 0 {
			return items
		}
		item := strings.Join(strings.Fields(value[start+1:start+end]), "")
		if item != "" && !strings.ContainsAny(item, "<") {
			items = append(items, item)
		}
		value = value[start+end+1:]
	}
}

// parseUnsubscribeMailto reads a mailto: URI with exactly one address. The subject and body
// hfields are kept; every other hfield, "to" among them, is ignored, so the URI cannot add
// recipients the person was not shown (RFC 6068 §7).
func parseUnsubscribeMailto(target *url.URL) (UnsubscribeMailto, bool) {
	addr, err := url.PathUnescape(target.Opaque)
	if err != nil || addr == "" || strings.ContainsAny(addr, ",\r\n") {
		return UnsubscribeMailto{}, false
	}
	parsed, err := stdmail.ParseAddress(addr)
	if err != nil || parsed.Name != "" || parsed.Address != addr || ValidateAddress(addr) != nil {
		return UnsubscribeMailto{}, false
	}
	m := UnsubscribeMailto{To: addr}
	// RFC 6068 §2 escapes with percent signs only: a "+" is a plus, not a space, so the
	// query is not read as a form.
	for _, field := range strings.Split(target.RawQuery, "&") {
		if field == "" {
			continue
		}
		rawKey, rawValue, _ := strings.Cut(field, "=")
		key, err := url.PathUnescape(rawKey)
		if err != nil {
			return UnsubscribeMailto{}, false
		}
		value, err := url.PathUnescape(rawValue)
		if err != nil {
			return UnsubscribeMailto{}, false
		}
		switch strings.ToLower(key) {
		case "subject":
			m.Subject = value
		case "body":
			m.Body = value
		}
	}
	// The subject becomes a header field, so a line break in it would start another one.
	if strings.ContainsAny(m.Subject, "\r\n") {
		return UnsubscribeMailto{}, false
	}
	return m, true
}
