package models

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"unsafe"

	"github.com/rest-mail/rest-mail-server/internal/messageseal"
)

// SourceReader reads the exact inline RFC 5322 source without copying it, preferring the
// octets when the source could not be stored as text.
func (message Message) SourceReader() io.Reader {
	if message.RawOctets != nil {
		return bytes.NewReader(message.RawOctets)
	}
	return strings.NewReader(message.RawMessage)
}

func (message Message) SealDigest() (string, error) {
	return message.SealDigestFrom(message.SourceReader())
}

// SealDigestFrom seals the supplied source and the same fields as an inline receipt.
func (message Message) SealDigestFrom(source io.Reader) (string, error) {
	fields := []string{message.MsgID, message.InReplyTo, message.References, message.Sender, message.SenderName, message.Subject, message.BodyText, message.BodyHTML}
	for _, field := range []JSONB{message.Headers, message.RecipientsTo, message.RecipientsCc} {
		if len(field) == 0 {
			field = JSONB("[]")
		}
		// Most rows are being stored and hold what encoding/json wrote, which is what this would
		// write again. Nothing writes to the field while it's read.
		if isCanonicalJSON(field) {
			fields = append(fields, unsafe.String(unsafe.SliceData(field), len(field)))
			continue
		}
		var value any
		decoder := json.NewDecoder(bytes.NewReader(field))
		decoder.UseNumber()
		if err := decoder.Decode(&value); err != nil {
			return "", err
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return "", err
		}
		fields = append(fields, string(encoded))
	}
	return messageseal.Digest(source, fields...)
}
