package imapmail

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"strconv"
	"time"

	"github.com/emersion/go-message"
	_ "github.com/emersion/go-message/charset"
	mail "github.com/emersion/go-message/mail"
	apitypes "github.com/rest-mail/rest-mail-server/pkg/apitypes"
)

type Attachment struct {
	ID          uint      `json:"id"`
	MessageID   uint      `json:"message_id"`
	Filename    string    `json:"filename"`
	ContentType string    `json:"content_type"`
	SizeBytes   int64     `json:"size_bytes"`
	Checksum    string    `json:"checksum"`
	CreatedAt   time.Time `json:"created_at"`
}

type attachmentRef struct {
	Message uint
	Index   int
}

func (store *Store) parseMessage(result *apitypes.Message, raw []byte) ([]Attachment, error) {
	reader, err := mail.CreateReader(bytes.NewReader(raw))
	if err != nil && !message.IsUnknownCharset(err) {
		return nil, errors.New("could not decode MIME message")
	}
	defer reader.Close()
	headers := map[string][]string{}
	for fields := reader.Header.Fields(); fields.Next(); {
		headers[fields.Key()] = append(headers[fields.Key()], fields.Value())
	}
	result.Headers, _ = json.Marshal(headers)
	result.References = reader.Header.Get("References")
	result.MsgID = reader.Header.Get("Message-ID")
	result.InReplyTo = reader.Header.Get("In-Reply-To")
	attachments := []Attachment{}
	for index := 0; ; index++ {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil && !message.IsUnknownCharset(err) {
			return nil, errors.New("could not decode MIME part")
		}
		data, err := io.ReadAll(io.LimitReader(part.Body, maxMessageSize+1))
		if err != nil || len(data) > maxMessageSize {
			return nil, errors.New("MIME part too large")
		}
		typeName, params, _ := mime.ParseMediaType(part.Header.Get("Content-Type"))
		if typeName == "" {
			typeName = "application/octet-stream"
		}
		if inline, ok := part.Header.(*mail.InlineHeader); ok && (typeName == "text/plain" || typeName == "text/html") {
			_, disposition, _ := mime.ParseMediaType(inline.Get("Content-Disposition"))
			if disposition["filename"] == "" && params["name"] == "" {
				if typeName == "text/plain" {
					result.BodyText += string(data)
				} else {
					result.BodyHTML += string(data)
				}
				continue
			}
		}
		filename := params["name"]
		if header, ok := part.Header.(*mail.AttachmentHeader); ok {
			filename, _ = header.Filename()
		}
		if filename == "" {
			filename = "attachment-" + strconv.Itoa(index+1)
		}
		checksum := sha256.Sum256(data)
		identity := sha256.Sum256([]byte(fmt.Sprintf("%d:%d", result.ID, index)))
		id := IDBase + uint(binary.BigEndian.Uint64(identity[:8])&uint64(IDBase-1))
		store.mu.Lock()
		ref := attachmentRef{Message: result.ID, Index: index}
		if old, exists := store.attachments[id]; exists && old != ref {
			store.mu.Unlock()
			return nil, errors.New("attachment identity collision")
		}
		store.attachments[id] = ref
		store.mu.Unlock()
		attachments = append(attachments, Attachment{ID: id, MessageID: result.ID, Filename: filename, ContentType: typeName, SizeBytes: int64(len(data)), Checksum: hex.EncodeToString(checksum[:]), CreatedAt: result.ReceivedAt})
	}
	result.HasAttachments = len(attachments) > 0
	return attachments, nil
}

func attachmentData(raw []byte, index int) ([]byte, error) {
	reader, err := mail.CreateReader(bytes.NewReader(raw))
	if err != nil && !message.IsUnknownCharset(err) {
		return nil, errors.New("could not decode MIME message")
	}
	defer reader.Close()
	for offset := 0; ; offset++ {
		part, err := reader.NextPart()
		if err != nil && !message.IsUnknownCharset(err) {
			return nil, errors.New("attachment not found")
		}
		if offset == index {
			return io.ReadAll(io.LimitReader(part.Body, maxMessageSize+1))
		}
	}
}
