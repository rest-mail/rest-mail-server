package models

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"

	"github.com/rest-mail/rest-mail-server/internal/messageseal"
)

func (message Message) SealDigest() (string, error) {
	var source io.Reader = strings.NewReader(message.RawMessage)
	if message.RawOctets != nil {
		source = bytes.NewReader(message.RawOctets)
	}
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
