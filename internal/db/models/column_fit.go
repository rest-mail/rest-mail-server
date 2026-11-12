package models

import (
	"crypto/sha256"
	"encoding/hex"
	"path"
	"strings"
	"unicode/utf8"

	rmail "github.com/rest-mail/rest-mail-server/internal/mail"
)

// A message's header fields are the sender's to write, at any length and in any octets, and the
// message has to be stored whatever they hold: PostgreSQL refuses a value over its column's limit,
// a NUL and invalid UTF-8, and refusing the row refuses the mail. So these columns hold a value fit
// for showing, and the source, which is stored exactly, holds what arrived (#1915).
const (
	attachmentFilenameColumn = 255
	attachmentTypeColumn     = 255
	senderColumn             = 255
	messageIDColumn          = 995
	calendarUIDColumn        = 512
	calendarOrganizerColumn  = 255
	calendarMethodColumn     = 32
	calendarStatusColumn     = 32
	// longestKeptExtension is how long a filename's last extension may be and still be kept when
	// the name is cut. What a file opens as is decided by its end, so a cut must not change that.
	longestKeptExtension = 16
)

// fitsColumn says a column limited to limit characters can store value. PostgreSQL counts a
// varchar's limit in characters.
func fitsColumn(value string, limit int) bool {
	return utf8.ValidString(value) && strings.IndexByte(value, 0) < 0 && utf8.RuneCountInString(value) <= limit
}

// storableText is value as PostgreSQL stores text: valid UTF-8 and no NUL, with U+FFFD for anything else.
func storableText(value string) string {
	if utf8.ValidString(value) && strings.IndexByte(value, 0) < 0 {
		return value
	}
	return strings.ToValidUTF8(strings.ReplaceAll(value, "\x00", "�"), "�")
}

// cutToCharacters is the first limit characters of value.
func cutToCharacters(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	count := 0
	for index := range value {
		if count == limit {
			return value[:index]
		}
		count++
	}
	return value
}

// fitText is value as a column limited to limit characters can store it.
func fitText(value string, limit int) string {
	return cutToCharacters(storableText(value), limit)
}

// FitFilename is an attachment's name as it's stored and answered: at most 255 characters of valid
// UTF-8. A longer name keeps its start and its last extension, so a.pdf and a.exe stay what they are.
func FitFilename(name string) string {
	name = storableText(name)
	if utf8.RuneCountInString(name) <= attachmentFilenameColumn {
		return name
	}
	extension := path.Ext(name)
	if utf8.RuneCountInString(extension) > longestKeptExtension {
		extension = ""
	}
	return cutToCharacters(name, attachmentFilenameColumn-utf8.RuneCountInString(extension)) + extension
}

// fitID is a Message-ID or thread ID as its column can store it. A cut ID would be some other ID,
// which could match some other message, so one too long to store is stored as its digest, which
// only the same ID gives: two messages naming it still thread together.
func fitID(id string) string {
	id = storableText(id)
	if utf8.RuneCountInString(id) <= messageIDColumn {
		return id
	}
	digest := sha256.Sum256([]byte(rmail.CanonicalID(id)))
	return "<" + hex.EncodeToString(digest[:]) + "@rest-mail.invalid>"
}

// fitColumns fits what a sender wrote to the columns that are limited. It runs before the seal, so
// the seal covers what's stored.
func (m *Message) fitColumns() {
	m.Sender = fitText(m.Sender, senderColumn)
	m.SenderName = fitText(m.SenderName, senderColumn)
	m.MsgID = fitID(m.MsgID)
	m.ThreadID = fitID(m.ThreadID)
	// No message here has an ID that long, so this replies to none of them. References still
	// threads it, by the same digest.
	if m.InReplyTo = storableText(m.InReplyTo); utf8.RuneCountInString(m.InReplyTo) > messageIDColumn {
		m.InReplyTo = ""
	}
}

// fitCalendarUID is an event's UID as its column can store it. As with fitID, a cut UID would be some
// other event's, and one version row's CANCEL would reach that event, so one too long to store is
// stored as its digest: every version of the event still shares it. The event in the message keeps
// the UID as written, which is what a reply names.
func fitCalendarUID(uid string) string {
	uid = storableText(uid)
	if utf8.RuneCountInString(uid) <= calendarUIDColumn {
		return uid
	}
	digest := sha256.Sum256([]byte(uid))
	return hex.EncodeToString(digest[:]) + "@rest-mail.invalid"
}

// fitColumns fits what a calendar's sender wrote to the version row's columns. A cut method or status
// stays longer than any this server acts on, so it can't become CANCEL or CANCELLED.
func (v *CalendarEventVersion) fitColumns() {
	v.UID = fitCalendarUID(v.UID)
	v.Method = fitText(v.Method, calendarMethodColumn)
	v.Status = fitText(v.Status, calendarStatusColumn)
	v.Summary = storableText(v.Summary)
	v.Organizer = fitText(v.Organizer, calendarOrganizerColumn)
}
