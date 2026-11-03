package server

import (
	"log/slog"
	"strings"

	"github.com/rest-mail/go-imap/pkg/imap"
	"github.com/rest-mail/go-imap/pkg/imap/internal/wire"
)

func (s *Session) handleCapability(tag string) {
	s.send("* CAPABILITY %s", s.capabilities())
	s.tagged(tag, "OK", "CAPABILITY completed")
}

// loginDisabled reports whether plaintext LOGIN / AUTHENTICATE is currently
// refused because the link must be encrypted first (RFC 2595, RFC 3501 §7.1.1).
// The policy is derived rather than a tunable knob (secure by construction):
// whenever a TLS config is present, credentials must travel encrypted, so a
// still-cleartext link is refused until STARTTLS upgrades it. With no TLS
// configured at all (development) there is nothing to require, and once the link
// is already encrypted the requirement is satisfied. This single predicate backs
// both the LOGINDISABLED advertisement and the LOGIN/AUTHENTICATE gates, so the
// advertised capability can never disagree with what the server will accept.
func (s *Session) loginDisabled() bool {
	return !s.usingTLS && s.tlsConfig != nil
}

// capabilities returns the space-separated CAPABILITY tokens for the session's
// current state. Extensions that depend on optional backend interfaces are
// included only once the concrete [Mailbox] is known (post-authentication) and
// only when it implements them, so the advertised list never over-promises.
func (s *Session) capabilities() string {
	// SPECIAL-USE (RFC 6154): LIST reports each folder's role, and takes the
	// SPECIAL-USE selection and return options.
	// LITERAL- (RFC 7888 §5): non-synchronizing literals up to 4096 octets.
	caps := []string{"IMAP4rev1", "LITERAL-", "IDLE", "MOVE", "QUOTA", "UNSELECT", "ENABLE", "SPECIAL-USE", "NAMESPACE", "ESEARCH", "LIST-STATUS", "STATUS=SIZE", "BINARY", "SEARCHRES", "LIST-EXTENDED"}
	if _, ok := s.mailbox.(imap.UIDPlusMailbox); ok {
		caps = append(caps, "UIDPLUS")
	}
	if _, ok := s.mailbox.(imap.UTF8Mailbox); ok {
		caps = append(caps, "UTF8=ACCEPT")
	}
	if !s.usingTLS && s.tlsConfig != nil {
		caps = append(caps, "STARTTLS")
	}
	// RFC 3501 §7.1.1 / RFC 2595: when the server refuses plaintext LOGIN until
	// TLS is established it MUST advertise LOGINDISABLED (and withhold the
	// AUTH=PLAIN it would reject), so clients upgrade via STARTTLS first. This
	// disappears once the link is encrypted.
	if s.loginDisabled() {
		caps = append(caps, "LOGINDISABLED")
	}
	if s.usingTLS || s.tlsConfig == nil {
		// SASL-IR (RFC 4959): AUTHENTICATE may carry the initial response.
		caps = append(caps, "SASL-IR", "AUTH=PLAIN")
	}
	return strings.Join(caps, " ")
}

// handleEnable implements ENABLE (RFC 5161). UTF8=ACCEPT (RFC 6855) is the
// only enable-able extension, and only when the Mailbox implements
// [UTF8Mailbox]. RFC 6855 §3 allows it only in the authenticated state, so it
// is ignored once a mailbox is selected. Every other requested capability is
// unknown and silently ignored per §3.1. Newly enabled names are echoed in an
// untagged "* ENABLED" response; one already enabled is not repeated.
// A successful command always sends ENABLED, even when the list is empty.
func (s *Session) handleEnable(tag, args string) {
	if !s.auth.authenticated {
		s.tagged(tag, "NO", "Not authenticated")
		return
	}
	// Capability names are atoms, not strings. Validate the whole list before
	// enabling anything so a later syntax error cannot partially change mode.
	names := strings.Split(args, " ")
	for _, name := range names {
		if !wire.ValidFlagKeyword(name) { // capability and flag-keyword both use atom
			s.tagged(tag, "BAD", "ENABLE requires capability atoms separated by SP")
			return
		}
	}
	var enabled []string
	for _, name := range names {
		if !strings.EqualFold(name, "UTF8=ACCEPT") || s.utf8 || s.selected != nil {
			continue
		}
		um, ok := s.mailbox.(imap.UTF8Mailbox)
		if !ok {
			continue
		}
		if err := um.EnableUTF8(); err != nil {
			slog.Warn("imap: enable utf8 failed", "error", err)
			s.tagged(tag, "NO", "ENABLE failed")
			return
		}
		s.utf8 = true
		enabled = append(enabled, "UTF8=ACCEPT")
	}
	if len(enabled) > 0 {
		s.send("* ENABLED %s", strings.Join(enabled, " "))
	} else {
		s.send("* ENABLED")
	}
	s.tagged(tag, "OK", "ENABLE completed")
}

// utf8Accept reports whether this session's backend supports UTF8=ACCEPT
// (RFC 6855 §3), the same test that puts the capability in CAPABILITY. Such a
// server takes UTF-8 in quoted-strings from then on and holds mailbox names to
// the Net-Unicode rules, whether or not the client has enabled the mode yet.
func (s *Session) utf8Accept() bool {
	_, ok := s.mailbox.(imap.UTF8Mailbox)
	return ok
}

// commandTextError enforces the typed-text encoding for the current session.
// RFC3501 CHAR is 7-bit; 8-bit data must be sent in a literal. RFC6855 extends
// quoted text only after authentication reveals a UTF8=ACCEPT backend. LOGIN
// is never extended for UTF-8 credentials; AUTHENTICATE carries them instead.
// Callers pass original line segments, never materialized literal contents.
func (s *Session) commandTextError(text string) string {
	if s.utf8Accept() {
		if !wire.ValidCommandUTF8(text) {
			return "Invalid UTF-8 in command"
		}
		return ""
	}
	for i := range len(text) {
		if text[i] >= 0x80 {
			return "8-bit text must be sent as a literal"
		}
	}
	return ""
}
