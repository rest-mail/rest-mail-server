package handlers

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gorm.io/gorm"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/grants"
	rmail "github.com/rest-mail/rest-mail-server/internal/mail"
)

// These speak only to the route, so they hold on any way of reading a request. The rows wanted
// here were recorded from the route as it stood when it held a whole request in memory.

const importedMultipart = "Message-ID: <multi@example.test>\r\n" +
	"From: =?UTF-8?Q?Caf=C3=A9?= <cafe@example.test>\r\nTo: To One <one@example.test>, two@example.test\r\nCc: cc@example.test\r\n" +
	"Subject: =?UTF-8?Q?Caf=C3=A9_menu?=\r\nDate: Thu, 02 Jan 2020 03:04:05 +0000\r\nIn-Reply-To: <parent@example.test>\r\nReferences: <root@example.test> <parent@example.test>\r\n" +
	"Authentication-Results: old.example; spf=pass\r\nContent-Type: multipart/mixed; boundary=OUT\r\n\r\n" +
	"--OUT\r\nContent-Type: multipart/alternative; boundary=IN\r\n\r\n" +
	"--IN\r\nContent-Type: text/plain; charset=iso-8859-1\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\ncaf=E9 plain\r\n" +
	"--IN\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<p>café html</p>\r\n--IN--\r\n" +
	"--OUT\r\nContent-Type: text/calendar; charset=utf-8\r\n\r\nBEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:one\r\nSUMMARY:Lunch\r\nDTSTART:20200102T030405Z\r\nDTEND:20200102T040405Z\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n" +
	"--OUT\r\nContent-Type: image/png\r\nContent-Disposition: inline; filename=pic.png\r\nContent-ID: <pic>\r\nContent-Transfer-Encoding: base64\r\n\r\ncGljdHVyZQ==\r\n" +
	"--OUT\r\nContent-Type: application/pdf; name=name.pdf\r\nContent-Transfer-Encoding: base64\r\n\r\nJVBERg==\r\n" +
	"--OUT\r\nContent-Type: application/octet-stream\r\nContent-Disposition: attachment; filename=\"a b.bin\"\r\n\r\nbinary\xff\x00\r\n" +
	"--OUT\r\nContent-Type: application/octet-stream\r\nContent-Disposition: attachment; filename=empty.bin\r\nContent-Transfer-Encoding: base64\r\n\r\n\r\n" +
	"--OUT\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment; filename=broken.pdf\r\nContent-Transfer-Encoding: base64\r\n\r\n!!!!\r\n" +
	"--OUT--\r\n"

var importedRowCases = []struct {
	name, message, extra string
	want                 string
}{
	{name: "text", message: "Message-ID: <text@example.test>\r\nFrom: Old <old@example.test>\r\nTo: to@example.test\r\nSubject: plain\r\nDate: Thu, 02 Jan 2020 03:04:05 +0000\r\n\r\nbody\r\n", want: `{"answered":false,"attachment_bytes":0,"attachments":[],"body_bytes":6,"body_html":"","body_text":"body\r\n","calendar":"[]","cc":"[]","date":"2020-01-02T03:04:05Z","downgraded_size":147,"draft":false,"flagged":false,"folder":"INBOX","has_attachments":false,"headers":"{\"To\": [\"to@example.test\"], \"Date\": [\"Thu, 02 Jan 2020 03:04:05 +0000\"], \"From\": [\"Old \u003cold@example.test\u003e\"], \"Subject\": [\"plain\"], \"Message-Id\": [\"\u003ctext@example.test\u003e\"]}","imap_deleted":false,"in_reply_to":"","keywords":null,"message_count":1,"message_id":"\u003ctext@example.test\u003e","pop3_utf8_size":147,"quota_used":11,"raw_is_text":true,"raw_sha256":"9eace67c0680bf65dcba73c529ae2283e4a6014c9e48fc4daf235c6859e28005","raw_size":147,"read":false,"received_at":"2020-01-02T03:04:05Z","references":"","sealed":"e262b65b99a5f3618135e99db36da48fdf2a277e8ca289691325312e1e77af8c","sender":"old@example.test","sender_name":"Old","size_bytes":11,"starred":false,"subject":"plain","subject_bytes":5,"thread_id":"\u003ctext@example.test\u003e","to":"[\"to@example.test\"]"}`},
	{name: "utf8", message: "Message-ID: <utf8@example.test>\r\nFrom: old@example.test\r\nSubject: café\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nbodé\r\n", want: `{"answered":false,"attachment_bytes":0,"attachments":[],"body_bytes":7,"body_html":"","body_text":"bodé\r\n","calendar":"[]","cc":"[]","date":"","downgraded_size":139,"draft":false,"flagged":false,"folder":"INBOX","has_attachments":false,"headers":"{\"From\": [\"old@example.test\"], \"Subject\": [\"café\"], \"Message-Id\": [\"\u003cutf8@example.test\u003e\"], \"Content-Type\": [\"text/plain; charset=utf-8\"]}","imap_deleted":false,"in_reply_to":"","keywords":null,"message_count":1,"message_id":"\u003cutf8@example.test\u003e","pop3_utf8_size":123,"quota_used":12,"raw_is_text":true,"raw_sha256":"19ab90f8643c700cf36cb712eeb1466a3b482e699945cd7f3f6c1c1431a64cac","raw_size":123,"read":false,"received_at":"2020-01-02T03:04:05Z","references":"","sealed":"809b58e0fe9cad129f060a9974fd494c090f05e8a31df25cab070b9dbea86d47","sender":"old@example.test","sender_name":"","size_bytes":12,"starred":false,"subject":"café","subject_bytes":5,"thread_id":"\u003cutf8@example.test\u003e","to":"[]"}`},
	{name: "not_utf8", message: "Message-ID: <latin@example.test>\r\nFrom: old@example.test\r\nSubject: caf\xe9\r\nContent-Type: text/plain; charset=iso-8859-1\r\nContent-Transfer-Encoding: 8bit\r\n\r\nbod\xe9\r\n", want: `{"answered":false,"attachment_bytes":0,"attachments":[],"body_bytes":7,"body_html":"","body_text":"bodé\r\n","calendar":"[]","cc":"[]","date":"","downgraded_size":181,"draft":false,"flagged":false,"folder":"INBOX","has_attachments":false,"headers":"{\"From\": [\"old@example.test\"], \"Subject\": [\"caf�\"], \"Message-Id\": [\"\u003clatin@example.test\u003e\"], \"Content-Type\": [\"text/plain; charset=iso-8859-1\"], \"Content-Transfer-Encoding\": [\"8bit\"]}","imap_deleted":false,"in_reply_to":"","keywords":null,"message_count":1,"message_id":"\u003clatin@example.test\u003e","pop3_utf8_size":160,"quota_used":13,"raw_is_text":false,"raw_sha256":"505cac6769df89364431c39df8a90a61734632c2391dafb53fe8830f6a5ca7a4","raw_size":160,"read":false,"received_at":"2020-01-02T03:04:05Z","references":"","sealed":"88fd474eb2e7135e7cbd18260978a04739fdf92e87eba08d945abb8a2760b548","sender":"old@example.test","sender_name":"","size_bytes":13,"starred":false,"subject":"caf�","subject_bytes":6,"thread_id":"\u003clatin@example.test\u003e","to":"[]"}`},
	{name: "nul", message: "Message-ID: <nul@example.test>\r\nFrom: old@example.test\r\nSubject: nul\r\n\r\nbo\x00dy\r\n", want: `{"answered":false,"attachment_bytes":0,"attachments":[],"body_bytes":9,"body_html":"","body_text":"bo�dy\r\n","calendar":"[]","cc":"[]","date":"","downgraded_size":79,"draft":false,"flagged":false,"folder":"INBOX","has_attachments":false,"headers":"{\"From\": [\"old@example.test\"], \"Subject\": [\"nul\"], \"Message-Id\": [\"\u003cnul@example.test\u003e\"]}","imap_deleted":false,"in_reply_to":"","keywords":null,"message_count":1,"message_id":"\u003cnul@example.test\u003e","pop3_utf8_size":79,"quota_used":12,"raw_is_text":false,"raw_sha256":"ddcbee2f579d1c28ca9f1fbc1e92cf8ed8160a7ced509de30714a96529cf028a","raw_size":79,"read":false,"received_at":"2020-01-02T03:04:05Z","references":"","sealed":"8922e5ebf018649812e11c65b5ebed31fce48c2b10a94262207eb02b3c3e5096","sender":"old@example.test","sender_name":"","size_bytes":12,"starred":false,"subject":"nul","subject_bytes":3,"thread_id":"\u003cnul@example.test\u003e","to":"[]"}`},
	{name: "no_message_id", message: "From: old@example.test\r\nSubject: none\r\n\r\nbody\r\n", want: `{"answered":false,"attachment_bytes":0,"attachments":[],"body_bytes":6,"body_html":"","body_text":"body\r\n","calendar":"[]","cc":"[]","date":"","downgraded_size":47,"draft":false,"flagged":false,"folder":"INBOX","has_attachments":false,"headers":"{\"From\": [\"old@example.test\"], \"Subject\": [\"none\"]}","imap_deleted":false,"in_reply_to":"","keywords":null,"message_count":1,"message_id":"\u003cimport.283884eb1c0a5981cf6953ad87dc79d2f0f3cbc84814119d2ceb97308d9bc3d0@rest-mail.invalid\u003e","pop3_utf8_size":47,"quota_used":10,"raw_is_text":true,"raw_sha256":"283884eb1c0a5981cf6953ad87dc79d2f0f3cbc84814119d2ceb97308d9bc3d0","raw_size":47,"read":false,"received_at":"2020-01-02T03:04:05Z","references":"","sealed":"9816ac6c9f50d2f9e53712d22890f138ac03baa4608619f9902eed9a03e2b8d8","sender":"old@example.test","sender_name":"","size_bytes":10,"starred":false,"subject":"none","subject_bytes":4,"thread_id":"\u003cimport.283884eb1c0a5981cf6953ad87dc79d2f0f3cbc84814119d2ceb97308d9bc3d0@rest-mail.invalid\u003e","to":"[]"}`},
	{name: "single_part_base64_that_breaks", message: "Message-ID: <broken@example.test>\r\nFrom: old@example.test\r\nContent-Type: text/plain\r\nContent-Transfer-Encoding: base64\r\n\r\naGVsbG8gd29ybGQh!!!!\r\n", want: `{"answered":false,"attachment_bytes":0,"attachments":[],"body_bytes":12,"body_html":"","body_text":"hello world!","calendar":"[]","cc":"[]","date":"","downgraded_size":144,"draft":false,"flagged":false,"folder":"INBOX","has_attachments":false,"headers":"{\"From\": [\"old@example.test\"], \"Message-Id\": [\"\u003cbroken@example.test\u003e\"], \"Content-Type\": [\"text/plain\"], \"Content-Transfer-Encoding\": [\"base64\"]}","imap_deleted":false,"in_reply_to":"","keywords":null,"message_count":1,"message_id":"\u003cbroken@example.test\u003e","pop3_utf8_size":144,"quota_used":12,"raw_is_text":true,"raw_sha256":"0414cb60cb3a0fd722508f29c20e7a96fbbc7b4fc2ef3134f3cf4819636e92e4","raw_size":144,"read":false,"received_at":"2020-01-02T03:04:05Z","references":"","sealed":"167571bfc47ad82bbf70b1c23a1ca4586414af96fd2f9139cae09d0e75f0feac","sender":"old@example.test","sender_name":"","size_bytes":12,"starred":false,"subject":"","subject_bytes":0,"thread_id":"\u003cbroken@example.test\u003e","to":"[]"}`},
	{name: "single_part_not_text", message: "Message-ID: <pdf@example.test>\r\nFrom: old@example.test\r\nContent-Type: application/pdf\r\n\r\n%PDF\r\n", want: `{"answered":false,"attachment_bytes":0,"attachments":[],"body_bytes":0,"body_html":"","body_text":"","calendar":"[]","cc":"[]","date":"","downgraded_size":95,"draft":false,"flagged":false,"folder":"INBOX","has_attachments":false,"headers":"{\"From\": [\"old@example.test\"], \"Message-Id\": [\"\u003cpdf@example.test\u003e\"], \"Content-Type\": [\"application/pdf\"]}","imap_deleted":false,"in_reply_to":"","keywords":null,"message_count":1,"message_id":"\u003cpdf@example.test\u003e","pop3_utf8_size":95,"quota_used":0,"raw_is_text":true,"raw_sha256":"824cfdeef992ce777b3248291c2f40d46885dbd947ff4d88608ffa763acf0065","raw_size":95,"read":false,"received_at":"2020-01-02T03:04:05Z","references":"","sealed":"d5540f4cc05c378d6f7883cfd63627757e0313ed896ec32f1e8c0034939bb3a0","sender":"old@example.test","sender_name":"","size_bytes":0,"starred":false,"subject":"","subject_bytes":0,"thread_id":"\u003cpdf@example.test\u003e","to":"[]"}`},
	{name: "multipart", message: importedMultipart, want: `{"answered":false,"attachment_bytes":19,"attachments":[{"Filename":"name.pdf","ContentType":"application/pdf","StorageType":"filesystem","Checksum":"315d429b7714cedb6ad04ac31240145257692630457f3c88253c5beceac76027","Ref":"315d429b7714cedb6ad04ac31240145257692630457f3c88253c5beceac76027.bin","Size":4},{"Filename":"a b.bin","ContentType":"application/octet-stream","StorageType":"filesystem","Checksum":"fcdbeaeb82861702e6c5bc86f11f162ea821de052c3898f663fbc372cf0c25ac","Ref":"fcdbeaeb82861702e6c5bc86f11f162ea821de052c3898f663fbc372cf0c25ac.bin","Size":8},{"Filename":"empty.bin","ContentType":"application/octet-stream","StorageType":"filesystem","Checksum":"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855","Ref":"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855.bin","Size":0},{"Filename":"pic.png","ContentType":"image/png","StorageType":"filesystem","Checksum":"2cea274d0bedc39ec4ab6ba9e59ec889e3ed6fb56a1cf088a64d9b383378dc97","Ref":"2cea274d0bedc39ec4ab6ba9e59ec889e3ed6fb56a1cf088a64d9b383378dc97.bin","Size":7}],"body_bytes":28,"body_html":"\u003cp\u003ecafé html\u003c/p\u003e","body_text":"café plain","calendar":"[{\"uid\": \"one\", \"dtend\": \"2020-01-02T04:04:05Z\", \"method\": \"\", \"all_day\": false, \"dtstamp\": \"0001-01-01T00:00:00Z\", \"dtstart\": \"2020-01-02T03:04:05Z\", \"summary\": \"Lunch\", \"sequence\": 0, \"organizer\": {\"address\": \"\"}}]","cc":"[\"cc@example.test\"]","date":"2020-01-02T03:04:05Z","downgraded_size":1503,"draft":false,"flagged":false,"folder":"INBOX","has_attachments":true,"headers":"{\"Cc\": [\"cc@example.test\"], \"To\": [\"To One \u003cone@example.test\u003e, two@example.test\"], \"Date\": [\"Thu, 02 Jan 2020 03:04:05 +0000\"], \"From\": [\"=?UTF-8?Q?Caf=C3=A9?= \u003ccafe@example.test\u003e\"], \"Subject\": [\"=?UTF-8?Q?Caf=C3=A9_menu?=\"], \"Message-Id\": [\"\u003cmulti@example.test\u003e\"], \"References\": [\"\u003croot@example.test\u003e \u003cparent@example.test\u003e\"], \"In-Reply-To\": [\"\u003cparent@example.test\u003e\"], \"Content-Type\": [\"multipart/mixed; boundary=OUT\"], \"X-Original-Authentication-Results\": [\"old.example; spf=pass\"]}","imap_deleted":false,"in_reply_to":"\u003cparent@example.test\u003e","keywords":null,"message_count":1,"message_id":"\u003cmulti@example.test\u003e","pop3_utf8_size":1503,"quota_used":57,"raw_is_text":false,"raw_sha256":"ca3d1e8d80000c7a3eea339ad558da9fbd81ddb1758e80833b66e5e1c810f8c6","raw_size":1503,"read":false,"received_at":"2020-01-02T03:04:05Z","references":"\u003croot@example.test\u003e \u003cparent@example.test\u003e","sealed":"15497544b902188cf58e0898ef0c9f50fb391d408cf6c460b4d4554464884f71","sender":"cafe@example.test","sender_name":"Café","size_bytes":38,"starred":false,"subject":"Café menu","subject_bytes":10,"thread_id":"\u003croot@example.test\u003e","to":"[\"one@example.test\", \"two@example.test\"]"}`},
	{name: "flags_and_keywords", message: "Message-ID: <flags@example.test>\r\nFrom: old@example.test\r\n\r\nbody\r\n", extra: `,"folder":"Archive","flags":["\\Seen","\\Answered","\\Flagged","\\Draft","\\Deleted","$Junk","$Forwarded"]`, want: `{"answered":true,"attachment_bytes":0,"attachments":[],"body_bytes":6,"body_html":"","body_text":"body\r\n","calendar":"[]","cc":"[]","date":"","downgraded_size":66,"draft":true,"flagged":true,"folder":"Archive","has_attachments":false,"headers":"{\"From\": [\"old@example.test\"], \"Message-Id\": [\"\u003cflags@example.test\u003e\"]}","imap_deleted":true,"in_reply_to":"","keywords":["$Junk","$Forwarded"],"message_count":1,"message_id":"\u003cflags@example.test\u003e","pop3_utf8_size":66,"quota_used":6,"raw_is_text":true,"raw_sha256":"3499b8087c769a55c3da70929e363d2434d9b09bccb0f608c3ce7f2cedebddbc","raw_size":66,"read":true,"received_at":"2020-01-02T03:04:05Z","references":"","sealed":"1c7230ec6ac3dcd3c75e31d1fcb4211d2aa5c16fc9b0943671c01621ffed8588","sender":"old@example.test","sender_name":"","size_bytes":6,"starred":true,"subject":"","subject_bytes":0,"thread_id":"\u003cflags@example.test\u003e","to":"[]"}`},
}

// importedRow is what is stored for a request: everything but ids, and where the storage root is.
func importedRow(t *testing.T, name, message, extra string) string {
	t.Helper()
	db := importProofDB(t)
	fx := seedAccountsFixture(t, db)
	if err := db.Create(&models.QuotaUsage{MailboxID: fx.user.ID}).Error; err != nil {
		t.Fatal(err)
	}
	if err := grants.EnsureImport(db, &fx.user); err != nil {
		t.Fatal(err)
	}
	h := NewMessageImportHandler(db, 1<<20, nil, nil)
	h.directory = t.TempDir()
	body := fmt.Sprintf(`{"raw_message":"%s","internal_date":"2020-01-02T03:04:05Z"%s}`, base64.StdEncoding.EncodeToString([]byte(message)), extra)
	r := authzRequest(http.MethodPost, fmt.Sprintf("/api/v1/accounts/%d/messages/import", fx.user.ID), fx.user.ID, nil, body)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	importMessageRoutes(h).ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("%s: %d %s", name, w.Code, w.Body)
	}
	var stored models.Message
	if err := db.Where("mailbox_id = ?", fx.user.ID).First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored.Source(), []byte(message)) {
		t.Fatalf("%s: source changed: %q", name, stored.Source())
	}
	var attachments []models.Attachment
	if err := db.Where("message_id = ?", stored.ID).Order("id").Find(&attachments).Error; err != nil {
		t.Fatal(err)
	}
	type attachment struct {
		Filename, ContentType, StorageType, Checksum, Ref string
		Size                                              int64
	}
	stores := []attachment{}
	for _, a := range attachments {
		stores = append(stores, attachment{a.Filename, a.ContentType, a.StorageType, a.Checksum, filepath.Base(a.StorageRef), a.SizeBytes})
	}
	var usage models.QuotaUsage
	if err := db.Where("mailbox_id = ?", fx.user.ID).First(&usage).Error; err != nil {
		t.Fatal(err)
	}
	var account models.Mailbox
	if err := db.First(&account, fx.user.ID).Error; err != nil {
		t.Fatal(err)
	}
	source := string(stored.Source())
	if stored.IMAPSize != rmail.IMAPWireSize(source, false) || stored.IMAPUTF8Size != rmail.IMAPWireSize(source, true) {
		t.Fatalf("imap_size %d and imap_utf8_size %d do not measure the stored source", stored.IMAPSize, stored.IMAPUTF8Size)
	}
	if stored.DowngradedSize != rmail.DowngradedSize(source) || stored.POP3UTF8Size != rmail.POP3WireSize(source) {
		t.Fatalf("downgraded_size %d and pop3_utf8_size %d do not measure the stored source", stored.DowngradedSize, stored.POP3UTF8Size)
	}
	dated := ""
	if stored.DateHeader != nil {
		dated = stored.DateHeader.UTC().Format("2006-01-02T15:04:05Z")
	}
	encoded, err := json.Marshal(map[string]any{
		"raw_is_text": stored.RawOctets == nil, "raw_sha256": fmt.Sprintf("%x", sha256.Sum256(stored.Source())), "raw_size": stored.RawSize, "downgraded_size": stored.DowngradedSize, "pop3_utf8_size": stored.POP3UTF8Size,
		"message_id": stored.MsgID, "in_reply_to": stored.InReplyTo, "references": stored.References, "thread_id": stored.ThreadID,
		"sender": stored.Sender, "sender_name": stored.SenderName, "to": string(stored.RecipientsTo), "cc": string(stored.RecipientsCc),
		"subject": stored.Subject, "body_text": stored.BodyText, "body_html": stored.BodyHTML, "size_bytes": stored.SizeBytes,
		"headers": string(stored.Headers), "calendar": string(stored.CalendarEventsRaw), "date": dated, "folder": stored.Folder,
		"received_at": stored.ReceivedAt.UTC().Format("2006-01-02T15:04:05Z"), "sealed": stored.SealedHash,
		"read": stored.IsRead, "answered": stored.IsAnswered, "flagged": stored.IsFlagged, "starred": stored.IsStarred, "draft": stored.IsDraft, "imap_deleted": stored.IMAPDeleted, "keywords": stored.Keywords,
		"has_attachments": stored.HasAttachments, "attachments": stores,
		"subject_bytes": usage.SubjectBytes, "body_bytes": usage.BodyBytes, "attachment_bytes": usage.AttachmentBytes, "message_count": usage.MessageCount, "quota_used": account.QuotaUsedBytes,
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func TestMessageImportStoredRows(t *testing.T) {
	for _, tc := range importedRowCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := importedRow(t, tc.name, tc.message, tc.extra); got != tc.want {
				t.Fatalf("stored\n%s\nwant\n%s", got, tc.want)
			}
		})
	}
}

// A message whose source can't be compared, and each way the same source can be stored.
func TestMessageImportComparesWithWhatIsStored(t *testing.T) {
	db := importProofDB(t)
	fx := seedAccountsFixture(t, db)
	if err := db.Create(&models.QuotaUsage{MailboxID: fx.user.ID}).Error; err != nil {
		t.Fatal(err)
	}
	if err := grants.EnsureImport(db, &fx.user); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&models.Mailbox{}).Where("id = ?", fx.user.ID).Update("quota_bytes", 1<<30).Error; err != nil {
		t.Fatal(err)
	}
	h := NewMessageImportHandler(db, 1<<20, nil, nil)
	h.directory = t.TempDir()
	routes := importMessageRoutes(h)
	send := func(message string) (imported bool, id uint) {
		t.Helper()
		body := fmt.Sprintf(`{"raw_message":"%s","folder":"Compare","internal_date":"2020-01-02T03:04:05Z"}`, base64.StdEncoding.EncodeToString([]byte(message)))
		r := authzRequest(http.MethodPost, fmt.Sprintf("/api/v1/accounts/%d/messages/import", fx.user.ID), fx.user.ID, nil, body)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		routes.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
		var answer struct {
			Data struct {
				ID       uint `json:"id"`
				Imported bool `json:"imported"`
			} `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &answer); err != nil {
			t.Fatal(err)
		}
		return answer.Data.Imported, answer.Data.ID
	}
	stored := func(messageID, text string, octets []byte) uint {
		t.Helper()
		row := models.Message{MailboxID: fx.user.ID, Folder: "Compare", MsgID: messageID, Sender: "s@example.test", RawMessage: text, RawOctets: octets}
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
		return row.ID
	}

	sourceless := stored("<none@example.test>", "", nil)
	if imported, id := send("Message-ID: <none@example.test>\r\n\r\nbody\r\n"); imported || id != sourceless {
		t.Errorf("a row with no source counts as this one: imported=%t id=%d, want %d", imported, id, sourceless)
	}

	text := "Message-ID: <asoctets@example.test>\r\n\r\nbody\r\n"
	asOctets := stored("<asoctets@example.test>", "", []byte(text))
	if imported, id := send(text); imported || id != asOctets {
		t.Errorf("the same bytes kept as octets: imported=%t id=%d, want %d", imported, id, asOctets)
	}

	utf := "Message-ID: <astext@example.test>\r\n\r\nbodé\r\n"
	asText := stored("<astext@example.test>", utf, nil)
	if imported, id := send(utf); imported || id != asText {
		t.Errorf("the same bytes kept as text: imported=%t id=%d, want %d", imported, id, asText)
	}

	binary := "Message-ID: <binary@example.test>\r\n\r\nbo\x00d\xff\r\n"
	if imported, _ := send(binary); !imported {
		t.Error("a new binary message wasn't stored")
	}
	if imported, _ := send(binary); imported {
		t.Error("a binary message was stored twice")
	}

	bare := stored("bare@example.test", "Message-ID: <bare@example.test>\r\n\r\nbody\r\n", nil)
	if imported, id := send("Message-ID: <bare@example.test>\r\n\r\nbody\r\n"); imported || id != bare {
		t.Errorf("an ID stored without brackets: imported=%t id=%d, want %d", imported, id, bare)
	}

	first, firstID := send("Message-ID: <edited@example.test>\r\n\r\none\r\n")
	second, secondID := send("Message-ID: <edited@example.test>\r\n\r\ntwo\r\n")
	if !first || !second || firstID == secondID {
		t.Errorf("the same ID with other octets is another message: %t %t %d %d", first, second, firstID, secondID)
	}
	if again, id := send("Message-ID: <edited@example.test>\r\n\r\ntwo\r\n"); again || id != secondID {
		t.Errorf("the second of two with one ID: imported=%t id=%d, want %d", again, id, secondID)
	}
}

// bigImport is a message of about size bytes, nearly all of it one attachment.
func bigImport(id string, size int) string {
	return "Message-ID: <" + id + "@example.test>\r\nFrom: old@example.test\r\nSubject: big\r\nContent-Type: multipart/mixed; boundary=B\r\n\r\n" +
		"--B\r\nContent-Type: text/plain\r\n\r\nsmall\r\n" +
		"--B\r\nContent-Type: application/octet-stream\r\nContent-Disposition: attachment; filename=big.bin\r\nContent-Transfer-Encoding: base64\r\n\r\n" +
		strings.Repeat(strings.Repeat("A", 76)+"\r\n", size/78) + "--B--\r\n"
}

// A rerun of mail already here, and mail the quota refuses, read no more than they must. Neither
// reads the source into memory; before, each held the request, the message, and its stored copy.
// The queries behind an answer allocate about 0.9 MiB whatever the size. The limit is above that
// and below one copy of the message, so a request that read the message into memory fails it.
func TestMessageImportNotStoredIsNotHeld(t *testing.T) {
	const (
		size  = 2 << 20
		limit = size * 3 / 4
	)
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
	h := NewMessageImportHandler(db, 2*size, nil, nil)
	h.directory = t.TempDir()
	routes := importMessageRoutes(h)
	send := func(message string) (*httptest.ResponseRecorder, uint64) {
		t.Helper()
		body := fmt.Sprintf(`{"raw_message":"%s","folder":"Big","internal_date":"2020-01-02T03:04:05Z"}`, base64.StdEncoding.EncodeToString([]byte(message)))
		r := authzRequest(http.MethodPost, fmt.Sprintf("/api/v1/accounts/%d/messages/import", fx.user.ID), fx.user.ID, nil, body)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		routes.ServeHTTP(w, r)
		runtime.ReadMemStats(&after)
		return w, after.TotalAlloc - before.TotalAlloc
	}
	message := bigImport("rerun", size)
	if w, _ := send(message); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"imported":true`) {
		t.Fatalf("first import: %d %s", w.Code, w.Body)
	}
	w, allocated := send(message)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"imported":false`) {
		t.Fatalf("rerun: %d %s", w.Code, w.Body)
	}
	t.Logf("a rerun of %d bytes allocated %d", size, allocated)
	if allocated > limit {
		t.Fatalf("a rerun of %d bytes allocated %d", size, allocated)
	}

	if err := db.Model(&models.Mailbox{}).Where("id = ?", fx.user.ID).Update("quota_bytes", 1).Error; err != nil {
		t.Fatal(err)
	}
	w, allocated = send(bigImport("full", size))
	if w.Code != http.StatusConflict {
		t.Fatalf("over quota: %d %s", w.Code, w.Body)
	}
	t.Logf("a refusal of %d bytes at the quota allocated %d", size, allocated)
	if allocated > limit {
		t.Fatalf("a refusal of %d bytes at the quota allocated %d", size, allocated)
	}
}

// Storing still reads the source, because the row keeps it (see importSource.columns), but the
// attachment isn't read for itself as well, and the request isn't held to get at either.
func TestMessageImportStoredHoldsOnlyWhatTheRowKeeps(t *testing.T) {
	const size = 1 << 20
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
	h := NewMessageImportHandler(db, 2*size, nil, nil)
	h.directory = t.TempDir()
	message := bigImport("stored", size)
	body := fmt.Sprintf(`{"raw_message":"%s","internal_date":"2020-01-02T03:04:05Z"}`, base64.StdEncoding.EncodeToString([]byte(message)))
	r := authzRequest(http.MethodPost, fmt.Sprintf("/api/v1/accounts/%d/messages/import", fx.user.ID), fx.user.ID, nil, body)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	routes := importMessageRoutes(h)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	routes.ServeHTTP(w, r)
	runtime.ReadMemStats(&after)
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	allocated := after.TotalAlloc - before.TotalAlloc
	t.Logf("storing %d bytes allocated %d, %.1f times over", len(message), allocated, float64(allocated)/float64(len(message)))
	// Three copies remain: the row's, one made sealing it (Message.BeforeCreate), and the
	// database driver's, plus about 2 MiB whatever the size. Measuring its sizes copies nothing.
	// Reading the whole request in memory came to 22.
	if allocated > 8*uint64(len(message)) {
		t.Fatalf("storing %d bytes allocated %d", len(message), allocated)
	}
}

// hostileFieldShapes are messages of one very long header field, which read once cost little of the
// limit, each with text. Plain ASCII beside many parameters is on purpose: with an 8-bit octet
// measuring the message also reads them in third_party/go-imap, which puts them over the limit when
// that package's pool is cold, as #1915 records.
func hostileFieldShapes(id string) map[string]string {
	shapes := allFieldShapes(id)
	for name := range fieldShapesStillOver {
		delete(shapes, name)
	}
	return shapes
}

// stillOverCost is what a shape may cost on each route, as a multiple of the message: its measured
// cost, rounded up with room for what a run varies by. 0 means the route holds it to 8x.
type stillOverCost struct{ importing, delivering, restmail uint64 }

// fieldShapesStillOver aren't reliably under 8x on the routes they name, measured from empty pools. For a
// field holding an 8-bit octet, most of what's left is what's stored and what measuring it reads again
// (WriteParameterField, the field unfolded each time it's read); those were 12 to 40x before #1968. The
// RESTMAIL rows at 9 are at 8.0x, where its structure check holds a long field a second time. The limit
// is still 8x (#1915), and each answers to it once it's under: holdsItsCost fails when one is.
var fieldShapesStillOver = map[string]stillOverCost{
	"8-bit charset*N, top level":   {importing: 18, delivering: 18, restmail: 18},
	"8-bit name*N":                 {importing: 12, delivering: 12, restmail: 13},
	"8-bit filename*N":             {importing: 11, delivering: 11, restmail: 11},
	"8-bit values, on a part":      {importing: 11, delivering: 11, restmail: 11},
	"8-bit parameters, on a part":  {importing: 11, delivering: 11, restmail: 11},
	"8-bit comments on filename*N": {importing: 11, delivering: 11, restmail: 11},
	"8-bit names, on many parts":   {importing: 9},
	"comments on name*N":           {restmail: 9},
	"name*N on a part":             {restmail: 9},
}

// shapesUnderTheLimit are the shapes a route holds to 8x, which is every one cost doesn't name for it.
func shapesUnderTheLimit(id string, cost func(stillOverCost) uint64) map[string]string {
	shapes := allFieldShapes(id)
	for name, over := range fieldShapesStillOver {
		if cost(over) != 0 {
			delete(shapes, name)
		}
	}
	return shapes
}

// emptyPools starts a measurement with nothing left warm by the one before. A pool of name sets is 1 to
// 2x of a long field, which is either side of 8x for some rows, so a row that's measured with it warm
// passes or fails by what a collection did. sync.Pool holds what it has through one collection.
func emptyPools() {
	runtime.GC()
	runtime.GC()
}

// holdsItsCost holds each shape a route still has over 8x to what it's measured at. send delivers a
// message and answers what that allocated. It fails when a row costs more, and when every try is clearly
// under 8x, at 7.5x or less, as the row then belongs back under the limit. A row at 8.0x varies either
// side of 8x by less than a run does, and that gap is what keeps it from failing by chance.
func holdsItsCost(t *testing.T, id string, cost func(stillOverCost) uint64, send func(t *testing.T, message string) uint64) {
	t.Helper()
	shapes := allFieldShapes(id)
	for name, over := range fieldShapesStillOver {
		ceiling := cost(over)
		if ceiling == 0 {
			continue
		}
		t.Run(name, func(t *testing.T) {
			best, worst, length := uint64(1)<<63, uint64(0), uint64(0)
			for attempt := range 3 {
				message := fmt.Sprintf("Message-ID: <still-over-%s-%d@example.test>\r\n", strings.ReplaceAll(name, " ", "-"), attempt) + shapes[name]
				length = uint64(len(message))
				emptyPools()
				allocated := send(t, message)
				best, worst = min(best, allocated), max(worst, allocated)
			}
			t.Logf("%d bytes allocated %.1f to %.1f times over, held to %dx", length, float64(best)/float64(length), float64(worst)/float64(length), ceiling)
			if best > ceiling*length {
				t.Fatalf("%d bytes allocated %d, over the %dx this row was measured at", length, best, ceiling)
			}
			if 2*worst <= 15*length {
				t.Fatalf("%d bytes allocated at most %d, under 8x: this row belongs back under the limit", length, worst)
			}
		})
	}
}

func TestStillOverTheLimit_ImportFields(t *testing.T) {
	const size = 1 << 20
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
	h := NewMessageImportHandler(db, 2*size, nil, nil)
	h.directory = t.TempDir()
	routes := importMessageRoutes(h)
	holdsItsCost(t, "From: old@example.test\r\n", func(c stillOverCost) uint64 { return c.importing }, func(t *testing.T, message string) uint64 {
		body := fmt.Sprintf(`{"raw_message":"%s","internal_date":"2020-01-02T03:04:05Z"}`, base64.StdEncoding.EncodeToString([]byte(message)))
		r := authzRequest(http.MethodPost, fmt.Sprintf("/api/v1/accounts/%d/messages/import", fx.user.ID), fx.user.ID, nil, body)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		routes.ServeHTTP(w, r)
		runtime.ReadMemStats(&after)
		if w.Code != http.StatusOK {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
		return after.TotalAlloc - before.TotalAlloc
	})
}

func TestStillOverTheLimit_DeliverFields(t *testing.T) {
	send, _, _ := deliverLimitSend(t)
	holdsItsCost(t, "From: old@example.test\r\nSubject: s\r\nDate: Thu, 02 Jan 2020 03:04:05 +0000\r\n", func(c stillOverCost) uint64 { return c.delivering }, func(t *testing.T, message string) uint64 {
		w, allocated := send(message)
		if w.Code != http.StatusCreated {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
		return allocated
	})
}

func allFieldShapes(id string) map[string]string {
	const size = 1 << 20
	long := func(first, each string, end string) string {
		var b strings.Builder
		b.WriteString(first)
		for i := 0; b.Len() < size*3/4; i++ {
			fmt.Fprintf(&b, each, i, i)
		}
		return b.String() + end + "\r\n"
	}
	eightBit := strings.Repeat("\u00f6", size/8) + "\r\n"
	ascii := strings.Repeat("hello world\r\n", size/52)
	// A boundary of many pieces. RFC 2231 numbers have to run in order, so what follows the gap isn't
	// part of it: the body can use the boundary, where one as long as all of them can't be read by the
	// multipart reader, whose line buffer can't hold it, and stores no text. The import test has that
	// one too.
	continued := long("Content-Type: multipart/mixed; boundary*0=B", ";\r\n boundary*2%d=v%d", "")
	// A repeated name is read now, where it used to stop the field being read at all, so what it costs
	// is new: on a field too short to read a parameter at a time, and on one long enough to.
	half := func(format string) string {
		var b strings.Builder
		for i := range size / 60 {
			fmt.Fprintf(&b, format, i)
		}
		return b.String()
	}
	repeatedPart := "--B\r\nContent-Type: a/b; x=1; x=2\r\nContent-Disposition: inline; x=1; x=2\r\n\r\n" + strings.Repeat("y", size/1400) + "\r\n"
	// Every reader of a field now reads past a comment and an 8-bit octet, so what those cost a field is
	// new. One octet makes the whole of a field 8-bit.
	onPart := func(field string) string {
		return id + "Content-Type: multipart/mixed; boundary=B\r\n\r\n--B\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n" + eightBit + "--B\r\n" + field + "\r\n\r\nx\r\n--B--\r\n"
	}
	return map[string]string{
		"comments, on a part":               onPart(long("Content-Type: application/x", ";\r\n p%d=v%d (c)", "")),
		"comments on name*N":                onPart(long("Content-Type: application/x", ";\r\n name*%d=v (c%.0d)", "")),
		"comments, top level":               id + long("Content-Type: text/plain; charset=utf-8", ";\r\n p%d=v%d (c)", "") + "\r\n" + eightBit,
		"Latin-1 filename*N":                onPart(long("Content-Disposition: attachment; filename*0*=iso-8859-1''%E9", ";\r\n filename*%d*=%%E9%.0d", "")),
		"comments on filename*N":            onPart(long("Content-Disposition: attachment", ";\r\n filename*%d=v (c%.0d)", "")),
		"8-bit parameters, on a part":       onPart(long("Content-Type: application/x; x=\u00e9", ";\r\n p%d=v%d", "")),
		"8-bit values, on a part":           onPart(long("Content-Disposition: attachment", ";\r\n p%d=\u00e9%d", "")),
		"8-bit name*N":                      onPart(long("Content-Type: application/x; x=\u00e9", ";\r\n name*%d=v%.0d", "")),
		"8-bit filename*N":                  onPart(long("Content-Disposition: attachment; x=\u00e9", ";\r\n filename*%d=v%.0d", "")),
		"8-bit charset*N, top level":        id + long("Content-Type: text/plain; x=\u00e9", ";\r\n charset*%d=v%.0d", "") + "\r\n" + eightBit,
		"8-bit comments on filename*N":      onPart(long("Content-Disposition: attachment; x=\u00e9", ";\r\n filename*%d=v (c%.0d)", "")),
		"a multipart that reads as text":    id + "Content-Type: multipart/mixed; boundary=B; x=\"b\r\n\r\n--B\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n" + strings.Repeat("\u00f6", size/2) + "\r\n--B--\r\n",
		"comments, on many parts":           id + "Content-Type: multipart/mixed; boundary=B\r\n\r\n--B\r\nContent-Type: text/plain\r\n\r\n" + eightBit + strings.Repeat("--B\r\nContent-Type: a/b (c); x=n (c)\r\nContent-Disposition: inline (c); x=f (c)\r\n\r\n"+strings.Repeat("y", size/1400)+"\r\n", 999) + "--B--\r\n",
		"8-bit names, on many parts":        id + "Content-Type: multipart/mixed; boundary=B\r\n\r\n--B\r\nContent-Type: text/plain\r\n\r\n" + eightBit + strings.Repeat("--B\r\nContent-Type: a/b; x=\u00e9\r\nContent-Disposition: inline; x=\u00e9\r\n\r\n"+strings.Repeat("y", size/1400)+"\r\n", 999) + "--B--\r\n",
		"repeated names, on many parts":     id + "Content-Type: multipart/mixed; boundary=B; boundary=C\r\n\r\n--B\r\nContent-Type: text/plain\r\n\r\n" + eightBit + strings.Repeat(repeatedPart, 999) + "--B--\r\n",
		"repeats of a kept name":            id + long("Content-Type: text/plain; charset=utf-8", ";\r\n charset=v%d%.0d", "") + "\r\n" + eightBit,
		"repeats of an RFC 2231 name":       id + long("Content-Type: text/plain; charset=utf-8; name*0=a", ";\r\n name*0=v%d%.0d", "") + "\r\n" + eightBit,
		"a blank, then only repeats":        id + "Content-Type: text/plain; charset=utf-8" + half(";\r\n p%d=valuevaluev") + ";\r\n ;" + strings.TrimPrefix(half(";\r\n p%d=othervalue"), ";") + "\r\n\r\n" + eightBit,
		"address list, top level":           id + long("To: to@example.test", ",\r\n \"Name %d\" <a%d@example.test>", "") + "Content-Type: text/plain; charset=utf-8\r\n\r\n" + eightBit,
		"parameters, top level":             id + long("Content-Type: text/plain; charset=utf-8", ";\r\n p%d=v%d", "") + "\r\n" + ascii,
		"parameters, on a part":             id + "Content-Type: multipart/mixed; boundary=B\r\n\r\n--B\r\n" + long("Content-Type: text/plain; charset=utf-8", ";\r\n p%d=v%d", "") + "\r\n" + ascii + "--B--\r\n",
		"a field past 1 MiB":                id + long("X-Long: y", "\r\n y%.0d%.0d", strings.Repeat("y", size/2)) + "\r\n" + ascii,
		"parameters, on many parts":         id + "Content-Type: multipart/mixed; boundary=B\r\n\r\n--B\r\nContent-Type: text/plain\r\n\r\n" + eightBit + strings.Repeat("--B\r\nContent-Type: a/b"+strings.Repeat("; p=v", size/5000)+"\r\n\r\nx\r\n", 750) + "--B--\r\n",
		"parameters, top level, 8-bit text": id + long("Content-Type: text/plain; charset=utf-8", ";\r\n p%d=v%d", "") + "\r\n" + eightBit,
		"parameters, on a part, 8-bit text": id + "Content-Type: multipart/mixed; boundary=B\r\n\r\n--B\r\n" + long("Content-Type: text/plain; charset=utf-8", ";\r\n p%d=v%d", "") + "\r\n" + eightBit + "--B--\r\n",
		"address list, encoded names":       id + long("To: to@example.test", ",\r\n =?utf-8?q?N%d?= <a%d@example.test>", "") + "Content-Type: text/plain; charset=utf-8\r\n\r\n" + eightBit,
		"address list, comments":            id + long("To: to@example.test", ",\r\n a%d@example.test (c%d)", "") + "Content-Type: text/plain; charset=utf-8\r\n\r\n" + eightBit,
		"address list, 8-bit names":         id + long("To: to@example.test", ",\r\n \"N\u00e4me %d\" <a%d@example.test>", "") + "Content-Type: text/plain; charset=utf-8\r\n\r\n" + eightBit,
		"address list, very short":          id + "To: a@b" + strings.Repeat(",a@b", size*3/16) + "\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n" + eightBit,
		"name*N on a part":                  id + "Content-Type: multipart/mixed; boundary=B\r\n\r\n--B\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n" + eightBit + "--B\r\n" + long("Content-Type: application/x", ";\r\n name*%d=v%.0d", "") + "\r\n\r\nx\r\n--B--\r\n",
		"filename*N on a part":              id + "Content-Type: multipart/mixed; boundary=B\r\n\r\n--B\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n" + eightBit + "--B\r\n" + long("Content-Disposition: attachment", ";\r\n filename*%d=v%.0d", "") + "\r\n\r\nx\r\n--B--\r\n",
		"charset*N, top level, 8-bit text":  id + long("Content-Type: text/plain", ";\r\n charset*%d=v%.0d", "") + "\r\n" + eightBit,
		"boundary*N, 8-bit text":            id + continued + "\r\n--B\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n" + eightBit + "--B--\r\n",
	}
}

// longBoundary is a Content-Type naming one boundary in pieces, numbered without a gap, to size.
func longBoundary(size int) string {
	var b strings.Builder
	b.WriteString("Content-Type: multipart/mixed")
	for i := 0; b.Len() < size; i++ {
		fmt.Fprintf(&b, ";\r\n boundary*%d=v%d", i, i)
	}
	return b.String()
}

// The same limit holds for an attachment FETCH has to convert, because measuring what it would
// send holds no copy of the message.
func TestMessageImportStoredHoldsTheLimitOnHostileShapes(t *testing.T) {
	importHoldsLimit(t, 8, func(name string) bool { return name != importRowStillOver && fieldShapesStillOver[name].importing == 0 })
}

// importRowStillOver is at 8.8x: nearly all of what's left is what's stored, see #1915. The limit
// is still 8x, and this row answers to it again once it's under.
const importRowStillOver = "address list, very short"

func TestStillOverTheLimit_ImportVeryShortList(t *testing.T) {
	importHoldsLimit(t, 9, func(name string) bool { return name == importRowStillOver })
}

// importHoldsLimit holds the rows it's told to under ceiling times their size. A row with a ceiling
// over 8 fails once it no longer needs that ceiling, so it can't be left there.
func importHoldsLimit(t *testing.T, ceiling uint64, included func(name string) bool) {
	t.Helper()
	const size = 1 << 20
	const id = "From: old@example.test\r\n"
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
	// What the first import in a process builds and keeps isn't the message's to answer for.
	warm := NewMessageImportHandler(db, 2*size, nil, nil)
	warm.directory = t.TempDir()
	first := authzRequest(http.MethodPost, fmt.Sprintf("/api/v1/accounts/%d/messages/import", fx.user.ID), fx.user.ID, nil,
		fmt.Sprintf(`{"raw_message":"%s","internal_date":"2020-01-02T03:04:05Z"}`, base64.StdEncoding.EncodeToString([]byte("Message-ID: <warm@example.test>\r\nFrom: old@example.test\r\n\r\nb\r\n"))))
	first.Header.Set("Content-Type", "application/json")
	importMessageRoutes(warm).ServeHTTP(httptest.NewRecorder(), first)
	shapes := allFieldShapes(id)
	for name, message := range map[string]string{
		"binary attachment":      id + "Subject: Gr\u00fc\u00dfe\r\nContent-Type: multipart/mixed; boundary=B\r\n\r\n--B\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nsm\u00e4ll\r\n--B\r\nContent-Type: application/octet-stream\r\nContent-Transfer-Encoding: binary\r\n\r\n" + strings.Repeat("\xf6", size) + "\r\n--B--\r\n",
		"base64, stray octets":   id + "Subject: Gr\u00fc\u00dfe\r\nContent-Type: multipart/mixed; boundary=B\r\n\r\n--B\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nsm\u00e4ll\r\n--B\r\nContent-Type: application/octet-stream\r\nContent-Transfer-Encoding: base64\r\n\r\n" + strings.Repeat("AAAA\xf6", size/5) + "\r\n--B--\r\n",
		"8-bit text, no charset": id + "Content-Type: text/plain\r\n\r\n" + strings.Repeat("\xf6", size) + "\r\n",
		"60 nested messages":     id + strings.Repeat("Content-Type: message/rfc822\r\n\r\n", 60) + "Subject: \xf6\r\n\r\n" + strings.Repeat("\xf6", size),
		"1000 typed parts":       id + "Content-Type: multipart/mixed; boundary=B\r\n\r\n" + strings.Repeat("--B\r\nContent-Type: a/b\r\n\r\n\xf6"+strings.Repeat("y", size/1000)+"\r\n", 1000) + "--B--\r\n",
		"boundary*N, all of it":  id + longBoundary(size*3/4) + "\r\n\r\n" + strings.Repeat("\xc3\xb6", size/8) + "\r\n",
	} {
		shapes[name] = message
	}
	for name, message := range shapes {
		if !included(name) {
			continue
		}
		t.Run(name, func(t *testing.T) {
			h := NewMessageImportHandler(db, 2*size, nil, nil)
			h.directory = t.TempDir()
			routes := importMessageRoutes(h)
			// TotalAlloc counts the whole process, so a run gets another try before it fails: a
			// message that really costs too much costs too much every time.
			best, length := uint64(1)<<63, 0
			for attempt := range 3 {
				stored := fmt.Sprintf("Message-ID: <%s-%d@example.test>\r\n", strings.ReplaceAll(name, " ", "-"), attempt) + message
				length = len(stored)
				body := fmt.Sprintf(`{"raw_message":"%s","internal_date":"2020-01-02T03:04:05Z"}`, base64.StdEncoding.EncodeToString([]byte(stored)))
				r := authzRequest(http.MethodPost, fmt.Sprintf("/api/v1/accounts/%d/messages/import", fx.user.ID), fx.user.ID, nil, body)
				r.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()
				var before, after runtime.MemStats
				emptyPools()
				runtime.ReadMemStats(&before)
				routes.ServeHTTP(w, r)
				runtime.ReadMemStats(&after)
				if w.Code != http.StatusOK {
					t.Fatalf("%d %s", w.Code, w.Body)
				}
				best = min(best, after.TotalAlloc-before.TotalAlloc)
				if best <= 8*uint64(length) {
					break
				}
			}
			t.Logf("storing %d bytes allocated %d, %.1f times over", length, best, float64(best)/float64(length))
			if best > ceiling*uint64(length) {
				t.Fatalf("storing %d bytes allocated %d", length, best)
			}
			if ceiling > 8 && best <= 8*uint64(length) {
				t.Fatalf("storing %d bytes allocated %d, under 8x: this row belongs back under the limit", length, best)
			}
			// The limit only means something if the row holds what it should, however it was written.
			var stored models.Message
			if err := db.Where("mailbox_id = ?", fx.user.ID).Order("id DESC").First(&stored).Error; err != nil {
				t.Fatal(err)
			}
			if name == "address list, top level" && strings.Count(string(stored.RecipientsTo), "@") < 1000 {
				t.Fatalf("recipients %.100s", stored.RecipientsTo)
			}
			if digest, err := stored.SealDigest(); err != nil || digest != stored.SealedHash {
				t.Fatalf("seal %q for %q (%v)", stored.SealedHash, digest, err)
			}
		})
	}
}

// The gateway's next hop holds the same limit on what the gateway sends: its request is a file
// until the message is read, once, and that one read parses it once and gives the row its text.
// Non-UTF-8 octets through this route are #1921's.
// deliverLimitSend is what a limit test of /deliver sends a message with, answering what it allocated.
func deliverLimitSend(t *testing.T) (func(message string) (*httptest.ResponseRecorder, uint64), *gorm.DB, models.Mailbox) {
	t.Helper()
	const id = "From: old@example.test\r\nSubject: s\r\nDate: Thu, 02 Jan 2020 03:04:05 +0000\r\n"
	db := importProofDB(t)
	if err := db.AutoMigrate(&models.Pipeline{}, &models.OutboundQueue{}); err != nil {
		t.Fatal(err)
	}
	domain := models.Domain{Name: "deliver-limit.test", Active: true}
	if err := db.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	mailbox := models.Mailbox{DomainID: domain.ID, LocalPart: "to", Address: "to@deliver-limit.test", Active: true, QuotaBytes: 1 << 40}
	if err := db.Create(&mailbox).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.QuotaUsage{MailboxID: mailbox.ID}).Error; err != nil {
		t.Fatal(err)
	}
	seedReceiveFixture(t, db, mailbox)
	route := deliveryRoute(NewMessageHandler(db, nil, nil, "", nil))
	send := func(message string) (*httptest.ResponseRecorder, uint64) {
		// What the SMTP gateway sends: the source, and from it the fields it reads, but not its text.
		body := fmt.Sprintf(`{"address":"%[1]s","sender":"old@example.test","sender_name":"Old","subject":"s","message_id":"m@example.test","recipients_to":["%[1]s"],"client_ip":"192.0.2.1","helo_name":"mail.example.test","received_tls":true,"tls_version":"TLS1.3","tls_cipher":"TLS_AES_128_GCM_SHA256","raw_message":"%[2]s"}`, mailbox.Address, base64.StdEncoding.EncodeToString([]byte(message)))
		r := httptest.NewRequest(http.MethodPost, "/api/v1/messages/deliver", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		var before, after runtime.MemStats
		emptyPools()
		runtime.ReadMemStats(&before)
		route(w, r)
		runtime.ReadMemStats(&after)
		return w, after.TotalAlloc - before.TotalAlloc
	}
	// What the first delivery in a process builds and keeps isn't the message's to answer for.
	if w, _ := send("Message-ID: <warm@example.test>\r\n" + id + "\r\nb\r\n"); w.Code != http.StatusCreated {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	return send, db, mailbox
}

func TestDeliverHoldsTheLimitOnHostileShapes(t *testing.T) {
	const size = 1 << 20
	const id = "From: old@example.test\r\nSubject: s\r\nDate: Thu, 02 Jan 2020 03:04:05 +0000\r\n"
	send, db, mailbox := deliverLimitSend(t)
	// A message nested in another isn't that message's text, so the shapes with parts carry a text
	// part beside them, of half the message.
	mixed := "Content-Type: multipart/mixed; boundary=B\r\n\r\n--B\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n" + strings.Repeat("\u00f6", size/4) + "\r\n"
	shapes := shapesUnderTheLimit(id, func(c stillOverCost) uint64 { return c.delivering })
	for name, message := range map[string]string{
		"repeated name, one long value": id + mixed + "--B\r\nContent-Type: a/b; p=" + strings.Repeat("y", size*3/4) + "; p=2\r\n\r\nx\r\n--B--\r\n",
		"plain ASCII":                   id + "Content-Type: text/plain\r\n\r\n" + strings.Repeat("hello world\r\n", size/13),
		"8-bit text, no charset":        id + "Content-Type: text/plain\r\n\r\n" + strings.Repeat("\u00f6", size/2) + "\r\n",
		"60 nested messages":            id + mixed + "--B\r\n" + strings.Repeat("Content-Type: message/rfc822\r\n\r\n", 60) + "Subject: \u00f6\r\n\r\n" + strings.Repeat("\u00f6", size/4) + "\r\n--B--\r\n",
		"1000 typed parts":              id + mixed + strings.Repeat("--B\r\nContent-Type: a/b\r\n\r\n\u00f6"+strings.Repeat("y", size/2000)+"\r\n", 999) + "--B--\r\n",
		"typed parts in a message":      id + mixed + "--B\r\nContent-Type: message/rfc822\r\n\r\nContent-Type: multipart/mixed; boundary=C\r\n\r\n" + strings.Repeat("--C\r\nContent-Type: a/b\r\n\r\nx\r\n", size/56) + "--C--\r\n--B--\r\n",
	} {
		shapes[name] = message
	}
	for name, message := range shapes {
		t.Run(name, func(t *testing.T) {
			// TotalAlloc counts the whole process, so a run gets another try before it fails: a
			// message that really costs too much costs too much every time.
			best, length := uint64(1)<<63, 0
			for attempt := range 3 {
				stored := fmt.Sprintf("Message-ID: <%s-%d@example.test>\r\n", strings.ReplaceAll(name, " ", "-"), attempt) + message
				length = len(stored)
				w, allocated := send(stored)
				if w.Code != http.StatusCreated {
					t.Fatalf("%d %s", w.Code, w.Body)
				}
				best = min(best, allocated)
				if best <= 8*uint64(length) {
					break
				}
			}
			t.Logf("delivering %d bytes allocated %d, %.1f times over", length, best, float64(best)/float64(length))
			if best > 8*uint64(length) {
				t.Fatalf("delivering %d bytes allocated %d", length, best)
			}
			// The limit only means something if the text was stored too.
			var stored models.Message
			if err := db.Where("mailbox_id = ?", mailbox.ID).Order("id DESC").First(&stored).Error; err != nil {
				t.Fatal(err)
			}
			if len(stored.BodyText) < size/4-64 {
				t.Fatalf("stored %d bytes of text", len(stored.BodyText))
			}
			if digest, err := stored.SealDigest(); err != nil || digest != stored.SealedHash {
				t.Fatalf("seal %q for %q (%v)", stored.SealedHash, digest, err)
			}
		})
	}
}
