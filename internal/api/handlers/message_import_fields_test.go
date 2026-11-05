package handlers

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/grants"
)

// importFieldsFixture posts to the real import route on an account that may import, and answers the
// row it stored, checked to be exact in its source and sealed over what it holds.
func importFieldsFixture(t *testing.T) func(t *testing.T, message string) (models.Message, []models.Attachment) {
	t.Helper()
	db := importProofDB(t)
	fx := seedAccountsFixture(t, db)
	if err := db.Create(&models.QuotaUsage{MailboxID: fx.user.ID}).Error; err != nil {
		t.Fatal(err)
	}
	if err := grants.EnsureImport(db, &fx.user); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&models.Mailbox{}).Where("id = ?", fx.user.ID).Update("quota_bytes", int64(1)<<40).Error; err != nil {
		t.Fatal(err)
	}
	h := NewMessageImportHandler(db, 8<<20, nil, nil)
	h.directory = t.TempDir()
	routes := importMessageRoutes(h)
	return func(t *testing.T, message string) (models.Message, []models.Attachment) {
		t.Helper()
		body := fmt.Sprintf(`{"raw_message":"%s","internal_date":"2020-01-02T03:04:05Z"}`, base64.StdEncoding.EncodeToString([]byte(message)))
		r := authzRequest(http.MethodPost, fmt.Sprintf("/api/v1/accounts/%d/messages/import", fx.user.ID), fx.user.ID, nil, body)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		routes.ServeHTTP(w, r)
		var answer struct {
			Data struct {
				ID       uint `json:"id"`
				Imported bool `json:"imported"`
			} `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &answer); err != nil || w.Code != http.StatusOK || !answer.Data.Imported {
			t.Fatalf("refused: %d %.300s (%v)", w.Code, w.Body, err)
		}
		var stored models.Message
		if err := db.First(&stored, answer.Data.ID).Error; err != nil {
			t.Fatal(err)
		}
		if string(stored.Source()) != message {
			t.Fatalf("source of %d bytes, sent %d", len(stored.Source()), len(message))
		}
		if digest, err := stored.SealDigest(); err != nil || digest != stored.SealedHash {
			t.Fatalf("seal %q, digest %q (%v)", stored.SealedHash, digest, err)
		}
		var attachments []models.Attachment
		if err := db.Where("message_id = ?", stored.ID).Find(&attachments).Error; err != nil {
			t.Fatal(err)
		}
		return stored, attachments
	}
}

// Import is the one route that stores attachments whatever the pipeline, and it reads a sender's
// fields straight from an old message: none of them may refuse the message.
func TestImportStoresAnyFieldASenderWrote(t *testing.T) {
	store := importFieldsFixture(t)
	const head = "From: Old <old@remote.test>\r\nSubject: s\r\nDate: Thu, 02 Jan 2020 03:04:05 +0000\r\n"
	part := func(fields string) string {
		return head + "Content-Type: multipart/mixed; boundary=B\r\n\r\n--B\r\nContent-Type: text/plain\r\n\r\nhello\r\n--B\r\n" + fields + "\r\n\r\nPDF\r\n--B--\r\n"
	}
	continued := func(parameter string, segments, each int, last string) string {
		var b strings.Builder
		for i := range segments {
			fmt.Fprintf(&b, ";\r\n %s*%d=\"%s\"", parameter, i, strings.Repeat("x", each))
		}
		fmt.Fprintf(&b, ";\r\n %s*%d=\"%s\"", parameter, segments, last)
		return b.String()
	}
	const pdf = "Content-Type: application/pdf"
	for _, tc := range []struct{ name, message, filename, mediaType string }{
		{"name*N, a little over the column", part(pdf + continued("name", 10, 30, ".pdf")), strings.Repeat("x", 251) + ".pdf", "application/pdf"},
		{"name*N, most of a megabyte", part(pdf + continued("name", 15000, 48, ".pdf")), strings.Repeat("x", 251) + ".pdf", "application/pdf"},
		{"filename*N, a little over the column", part(pdf + "\r\nContent-Disposition: attachment" + continued("filename", 10, 30, ".exe")), strings.Repeat("x", 251) + ".exe", "application/pdf"},
		{"a NUL in the filename", part(pdf + "\r\nContent-Disposition: attachment; filename*=utf-8''a%00b.pdf"), "a�b.pdf", "application/pdf"},
		{"invalid UTF-8 in the filename", part(pdf + "\r\nContent-Disposition: attachment; filename*=utf-8''a%FFb.pdf"), "a�b.pdf", "application/pdf"},
		{"8-bit octets in the filename", part(pdf + "\r\nContent-Disposition: attachment; filename=\"a\xe9b.pdf\""), "a�b.pdf", "application/pdf"},
		{"a media type past RFC 6838's", part("Content-Type: application/" + strings.Repeat("z", 300) + "\r\nContent-Disposition: attachment; filename=a.bin"), "a.bin", "application/octet-stream"},
		{"a long sender name", "From: " + strings.Repeat("N", 300) + " <old@remote.test>\r\nSubject: s\r\n\r\nb\r\n", "", ""},
		{"a long sender address", "From: " + strings.Repeat("a", 300) + "@remote.test\r\nSubject: s\r\n\r\nb\r\n", "", ""},
		{"a long In-Reply-To", "From: old@remote.test\r\nIn-Reply-To: <" + strings.Repeat("r", 1200) + "@x.test>\r\n\r\nb\r\n", "", ""},
		{"a long first reference", "From: old@remote.test\r\nReferences: <" + strings.Repeat("r", 3000) + "@x.test>\r\n\r\nb\r\n", "", ""},
		{"a NUL in a calendar", "From: old@remote.test\r\nContent-Type: text/calendar; method=REQUEST\r\nContent-Transfer-Encoding: base64\r\n\r\n" +
			base64.StdEncoding.EncodeToString([]byte("BEGIN:VCALENDAR\r\nMETHOD:REQUEST\r\nBEGIN:VEVENT\r\nUID:a\x00b\r\nSUMMARY:c\x00d\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")) + "\r\n", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			message := fmt.Sprintf("Message-ID: <%x@remote.test>\r\n", tc.name) + tc.message
			_, attachments := store(t, message)
			if tc.filename == "" {
				return
			}
			if len(attachments) != 1 || attachments[0].Filename != tc.filename || attachments[0].ContentType != tc.mediaType {
				t.Fatalf("attachments %+v", attachments)
			}
			if answered := attachmentOf(attachments[0]); answered.Filename != tc.filename {
				t.Fatalf("answered %q", answered.Filename)
			}
		})
	}
}

// A name the column holds has to download under that name: its Content-Disposition is built from it.
func TestLongestFilenameDownloadsUnderItsName(t *testing.T) {
	for _, name := range []string{models.FitFilename(strings.Repeat("x", 700_000) + ".pdf"), models.FitFilename(strings.Repeat("日", 1000) + ".pdf")} {
		_, parameters, err := mime.ParseMediaType(contentDispositionAttachment(name))
		if err != nil {
			t.Fatal(err)
		}
		if parameters["filename"] != name {
			t.Fatalf("downloads as %q, stored as %q", parameters["filename"], name)
		}
	}
}

// Import stores no delivery rules, but a long field beside a signal still leaves the class and the
// thread as they'd be without it, and a field past go-message's old limit is still stored.
func TestImportLongFieldHidesNoSignal(t *testing.T) {
	store := importFieldsFixture(t)
	const fixed = "From: old@remote.test\r\nSubject: s\r\nDate: Thu, 02 Jan 2020 03:04:05 +0000\r\n"
	long := func(size int) string {
		return "X-Long: " + strings.Repeat("yyyyyyy\r\n ", size/10) + "y\r\n"
	}
	for _, size := range []struct {
		name         string
		size         int
		keepsHeaders bool
	}{{"under the column rule", 60 << 10, true}, {"over the column rule", 90 << 10, false}, {"most of 1 MiB", 900 << 10, false}, {"past 1 MiB", 1200 << 10, false}} {
		for _, signal := range []struct{ field, class string }{
			{"List-Id: News <news.remote.test>", "newsletter"},
			{"List-Unsubscribe: <https://remote.test/leave>", "newsletter"},
			{"Auto-Submitted: auto-generated", "notification"},
			{"Precedence: bulk", "notification"},
			{"X-Plain: nothing", "person"},
		} {
			for _, longFirst := range []bool{true, false} {
				t.Run(fmt.Sprintf("%s/%s/long first %v", size.name, signal.field, longFirst), func(t *testing.T) {
					fields := signal.field + "\r\n" + long(size.size)
					if longFirst {
						fields = long(size.size) + signal.field + "\r\n"
					}
					stored, _ := store(t, fmt.Sprintf("Message-ID: <%x@remote.test>\r\n", t.Name())+fixed+fields+"\r\nb\r\n")
					answered := messageOf(stored)
					if answered.Class != signal.class {
						t.Fatalf("class %s, want %s", answered.Class, signal.class)
					}
					// A row with none answers an empty list, as every /deliver row always has.
					if kept := string(answered.Headers) != "[]"; kept != size.keepsHeaders {
						t.Fatalf("headers %.40s, kept: %v", answered.Headers, size.keepsHeaders)
					}
				})
			}
		}
		t.Run(size.name+"/threads with its parent", func(t *testing.T) {
			parentID := fmt.Sprintf("%x@remote.test", t.Name())
			parent, _ := store(t, "Message-ID: <"+parentID+">\r\n"+fixed+"\r\nb\r\n")
			for _, reply := range []string{"In-Reply-To: <" + parentID + ">", "References: <" + parentID + ">"} {
				for index, fields := range []string{long(size.size) + reply + "\r\n", reply + "\r\n" + long(size.size)} {
					stored, _ := store(t, fmt.Sprintf("Message-ID: <%x@remote.test>\r\n", fmt.Sprint(t.Name(), reply, index))+fixed+fields+"\r\nb\r\n")
					if stored.ThreadID != parent.ThreadID {
						t.Fatalf("%s: thread %.60s, want %.60s", reply, stored.ThreadID, parent.ThreadID)
					}
				}
			}
		})
	}
}
