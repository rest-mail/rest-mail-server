package imap

import (
	"bytes"
	"fmt"
	"math"
	"strings"

	"github.com/restmail/restmail/internal/gateway/apiclient"
	"github.com/restmail/restmail/internal/gateway/rawmsg"
	rmail "github.com/restmail/restmail/internal/mail"
	rmime "github.com/restmail/restmail/internal/mime"
)

// maxFolderNameLen bounds an IMAP folder (mailbox) name: the API's rule, so an
// oversized, attacker-chosen name never reaches the downstream API request.
const maxFolderNameLen = rmail.MaxFolderNameLen

// validateFolder is the gateway's object-level authorization guard for
// folder-scoped IMAP operations (OSI-9). Every folder name a client supplies —
// on SELECT/STATUS (Messages), COPY/MOVE and APPEND destinations — passes
// through here before it is used to build an API request path or JSON body.
//
// The backend already scopes each folder query to the authenticated account's
// mailbox (resolveAccountMailbox), so this is defense in depth: it rejects
// names that could smuggle control characters or CR/LF into the downstream
// request (header/JSON injection), traverse outside the intended path, or blow
// past the storage column width.
//
// The rule is the one the API applies when a folder is created or renamed
// (rmail.FolderNameProblem): valid UTF-8 of at most maxFolderNameLen bytes, not
// blank and not beginning or ending with a space, with no control characters
// (which includes NUL, TAB, CR and LF) and no path-traversal ("..") sequence.
// Ordinary hierarchical names ("INBOX", "Sent", "Work/Projects",
// "[Gmail]/All Mail") are accepted; only structurally dangerous names are
// refused.
func validateFolder(folder string) error {
	if problem := rmail.FolderNameProblem(folder); problem != "" {
		return fmt.Errorf("imap: folder name %s", problem)
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

// normalizeToCRLF rewrites bare LF line endings to CRLF, leaving existing CRLF
// intact (idempotent). It is applied to raw bytes accepted at APPEND ingest so
// the stored message is CRLF-framed: an LF-only message would otherwise break
// POP3 RETR/TOP framing (go-pop3 splits on CRLF, and bare-LF dot-lines are not
// dot-stuffed, risking premature termination) and diverge from the CRLF wire
// form every other stored message uses. It collapses CRLF to LF first, then
// expands every LF to CRLF, so already-CRLF input is unchanged and a mixed
// message is made uniform without doubling terminators.
func normalizeToCRLF(b []byte) []byte {
	b = bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
	b = bytes.ReplaceAll(b, []byte("\n"), []byte("\r\n"))
	return b
}

// buildRawMessage renders a stored message as RFC 5322 bytes for FETCH.
//
// The POP3 gateway hands a client the same stored message on RETR, so the two
// must agree byte for byte: both delegate to rawmsg.Build rather than keeping
// their own copy of the construction rules.
func buildRawMessage(msg apiclient.MessageDetail) string {
	return rawmsg.Build(msg)
}

// parseBasicHeaders extracts the structured delivery fields from a raw RFC 5322
// message on APPEND ingest.
//
// Parsing is delegated to internal/mime (go-message) — the same package the SMTP
// gateway and the REST ingest path use — so a message stored via APPEND yields
// the same fields as the same message arriving any other way: RFC 2047
// encoded-words decoded, transfer encodings decoded, charsets converted, and the
// text and HTML alternatives separated instead of handed back as one blob of raw
// MIME with the boundaries still in it (issue #298).
//
// The Message-ID is stored bare, without its angle brackets, matching the SMTP
// path. Keeping them here produced "<<id>>" when the message was rendered back
// into a header by rawmsg.Build.
func parseBasicHeaders(data []byte) (subject, bodyText, bodyHTML, messageID, senderName string) {
	email, err := rmime.Parse(data)
	if err != nil || email == nil {
		return "", "", "", "", ""
	}

	subject = email.Headers.Subject
	messageID = strings.Trim(email.Headers.MessageID, "<>")
	if from := email.Headers.From; len(from) > 0 {
		senderName = from[0].Name
	}
	bodyText, bodyHTML = rmime.TextAndHTML(email.Body)
	return
}
