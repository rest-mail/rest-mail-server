package api

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/attachmentstore/s3test"
	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
)

// TestAttachmentDownloadNamesTheFileAsTheSenderSpelledIt is #1886's filename, through the whole stack:
// a message whose attachment name is an RFC 2231 parameter in a charset other than UTF-8 goes through
// the production delivery route and its inbound pipeline into PostgreSQL and the attachment store, and
// is then downloaded through the production router. The Content-Disposition of that response has to
// carry the decoded name in the RFC 6266 / RFC 5987 form, and the body has to be the attachment's
// octets. The expected names are the charsets' own code points, not this server's output.
func TestAttachmentDownloadNamesTheFileAsTheSenderSpelledIt(t *testing.T) {
	// The server's own attachment store setting, here an S3 wire fixture, so the part is stored where
	// the download route reads it and no path check is touched.
	store := s3test.New(t)
	f := newCredentialFixtureWith(t, &config.Config{Environment: "test", SMTPMaxMessageSize: config.DefaultSMTPMaxMessageSize, Attachments: store.Config})
	var domain models.Domain
	if err := f.db.First(&domain, "name = ?", "example.test").Error; err != nil {
		t.Fatal(err)
	}
	// The server binds this filter to the configured store; the pipeline names no location of its own.
	filters := `[{"name":"extract_attachments","type":"transform","enabled":true}]`
	if err := f.db.Create(&models.Pipeline{DomainID: domain.ID, Direction: "inbound", Filters: json.RawMessage(filters), Active: true}).Error; err != nil {
		t.Fatal(err)
	}

	for _, row := range []struct {
		name, disposition, filename, encoded string
	}{
		{"Latin-2", "attachment; filename*=iso-8859-2''%A3%F3d%BC.txt", "Łódź.txt", "%C5%81%C3%B3d%C5%BA.txt"},
		{"Windows-1252, with the euro sign", "attachment; filename*=windows-1252''J%F6rg%80.bin", "Jörg€.bin", "J%C3%B6rg%E2%82%AC.bin"},
		{"continued Latin-2 segments", "attachment; filename*0*=iso-8859-2''%A3%F3;\r\n filename*1*=d%BC.bin", "Łódź.bin", "%C5%81%C3%B3d%C5%BA.bin"},
	} {
		t.Run(row.name, func(t *testing.T) {
			// Each attachment's octets differ, as a stored part is shared by its content.
			content := []byte("\x00\xff octets of " + row.name + "\r\n\x80")
			message := repeatedHead + "Content-Type: multipart/mixed; boundary=a\r\n\r\n" +
				"--a\r\nContent-Type: text/plain\r\n\r\nvisible text\r\n" +
				"--a\r\nContent-Type: application/octet-stream\r\nContent-Transfer-Encoding: base64\r\nContent-Disposition: " + row.disposition + "\r\n\r\n" +
				base64.StdEncoding.EncodeToString(content) + "\r\n--a--\r\n"
			payload, err := json.Marshal(map[string]any{"address": f.address, "sender": "sender@remote.test", "recipients_to": []string{f.address},
				"client_ip": "192.0.2.1", "helo_name": "mail.remote.test", "received_tls": true, "raw_message": base64.StdEncoding.EncodeToString([]byte(message))})
			if err != nil {
				t.Fatal(err)
			}
			if answer := f.do(http.MethodPost, "/api/v1/messages/deliver", "", string(payload)); answer.Code != http.StatusCreated {
				t.Fatalf("refused: %d %s", answer.Code, answer.Body)
			}
			stored := lastStored(t, f, message)

			var rows []models.Attachment
			if err := f.db.Where("message_id = ?", stored.ID).Find(&rows).Error; err != nil {
				t.Fatal(err)
			}
			if len(rows) != 1 || rows[0].Filename != row.filename {
				t.Fatalf("attachment rows %+v, want one named %q", rows, row.filename)
			}

			answer := f.do(http.MethodGet, fmt.Sprintf("/api/v1/attachments/%d", rows[0].ID), f.session, "")
			if answer.Code != http.StatusOK {
				t.Fatalf("download %d %.300s", answer.Code, answer.Body)
			}
			disposition := answer.Header().Get("Content-Disposition")
			// RFC 6266 §4.3 / RFC 5987: the name is an ext-value, so the header holds no raw or encoded-word text.
			if want := "attachment; filename*=utf-8''" + row.encoded; !strings.EqualFold(disposition, want) {
				t.Errorf("Content-Disposition %q, want %q", disposition, want)
			}
			if kind, params, err := mime.ParseMediaType(disposition); err != nil || kind != "attachment" || params["filename"] != row.filename {
				t.Errorf("Content-Disposition %q reads as %q %q (%v), want attachment named %q", disposition, kind, params, err, row.filename)
			}
			if !bytes.Equal(answer.Body.Bytes(), content) {
				t.Errorf("download body %q, want %q", answer.Body.Bytes(), content)
			}
		})
	}
}
