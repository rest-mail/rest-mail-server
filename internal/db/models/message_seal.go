package models

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"

	"github.com/rest-mail/rest-mail-server/internal/messageseal"
)

// SourceReader reads the exact stored RFC 5322 source without copying it, preferring the
// octets when the source could not be stored as text.
func (message Message) SourceReader() io.Reader {
	if message.RawOctets != nil {
		return bytes.NewReader(message.RawOctets)
	}
	return strings.NewReader(message.RawMessage)
}

func (message Message) SealDigest() (string, error) {
	source := message.SourceReader()
	fields := []string{message.MsgID, message.InReplyTo, message.References, message.Sender, message.SenderName, message.Subject, message.BodyText, message.BodyHTML}
	for _, field := range []JSONB{message.Headers, message.RecipientsTo, message.RecipientsCc} {
		if len(field) == 0 {
			field = JSONB("[]")
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
