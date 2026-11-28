package handlers

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/attachmentstore"
	"github.com/rest-mail/rest-mail-server/internal/attachmentstore/s3test"
	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/grants"
	rmime "github.com/rest-mail/rest-mail-server/internal/mime"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
	"github.com/rest-mail/rest-mail-server/internal/pipeline/filters"
	"gorm.io/gorm/logger"
)

// A delivered message's rows keep each part's disposition and Content-ID, and its empty
// part, and a forward of it is made from those rows alone: its inline image keeps the
// cid: its HTML names, its two identical attachments stay two, and its empty part goes
// with it (#2107).
func TestDeliveredPartsKeepTheirMetadataThroughAForward(t *testing.T) {
	db := openMsgDeleteTestDB(t)
	if err := migrateModels(db, &models.QuotaUsage{}, &models.Pipeline{}, &models.SieveScript{}, &models.OutboundQueue{}, &models.Contact{}); err != nil {
		t.Fatal(err)
	}
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })
	fx := seedMsgDeleteFixture(t, tx, 0)
	if err := tx.Create(&models.QuotaUsage{MailboxID: fx.mailbox.ID}).Error; err != nil {
		t.Fatal(err)
	}
	fc, err := json.Marshal([]pipeline.FilterConfig{{Name: "extract_attachments", Type: pipeline.FilterTypeTransform, Enabled: true}})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Create(&models.Pipeline{DomainID: fx.mailbox.DomainID, Direction: "inbound", Active: true, Filters: fc}).Error; err != nil {
		t.Fatal(err)
	}
	store := attachmentstore.NewS3(s3test.New(t).Config)
	registry := withQuietVacation(pipeline.NewRegistry())
	registry.Register("extract_attachments", filters.NewExtractAttachmentsWithS3(store))
	h := NewMessageHandler(tx, nil, pipeline.NewEngine(registry, nil), "", nil)
	setTestMessageSources(t, h)
	h.SetForwardStorage(1<<20, store)
	raw := "From: sender@remote.test\r\nTo: owner@delq.test\r\nSubject: parts\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=part\r\n\r\n" +
		"--part\r\nContent-Type: text/html\r\n\r\n<p><img src=\"cid:logo@remote.test\"></p>\r\n" +
		"--part\r\nContent-Type: image/png\r\nContent-Disposition: inline; filename=logo.png\r\nContent-ID: <logo@remote.test>\r\nContent-Transfer-Encoding: base64\r\n\r\naW1hZ2U=\r\n" +
		"--part\r\nContent-Type: application/octet-stream\r\nContent-Disposition: attachment; filename=same.bin\r\nContent-Transfer-Encoding: base64\r\n\r\ncGF5bG9hZA==\r\n" +
		"--part\r\nContent-Type: application/octet-stream\r\nContent-Disposition: attachment; filename=same.bin\r\nContent-Transfer-Encoding: base64\r\n\r\ncGF5bG9hZA==\r\n" +
		"--part\r\nContent-Type: text/plain\r\nContent-Disposition: attachment; filename=empty.txt\r\n\r\n\r\n" +
		"--part--\r\n"
	msg, err := h.deliverToLocal(context.Background(), localDeliveryParams{Mailbox: fx.mailbox, Sender: "sender@remote.test", Subject: "parts", BodyHTML: "<p></p>", RawMessage: raw})
	if err != nil {
		t.Fatal(err)
	}
	var rows []models.Attachment
	if err := tx.Where("message_id = ?", msg.ID).Order("id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	type meta struct {
		name, disposition, cid string
		size                   int64
	}
	got := map[meta]int{}
	for _, row := range rows {
		got[meta{row.Filename, row.Disposition, row.ContentID, row.SizeBytes}]++
	}
	want := map[meta]int{{"logo.png", "inline", "<logo@remote.test>", 5}: 1, {"same.bin", "attachment", "", 7}: 2, {"empty.txt", "attachment", "", 0}: 1}
	if len(rows) != 4 || len(got) != len(want) {
		t.Fatalf("delivered rows: %+v", rows)
	}
	for key, n := range want {
		if got[key] != n {
			t.Fatalf("delivered rows %+v: want %d of %+v", rows, n, key)
		}
	}

	// The stored message is not read for the parts: what it says no longer matters.
	if err := tx.Model(&models.Message{}).Where("id = ?", msg.ID).Update("raw_message", "Subject: not a MIME message\r\n\r\nnothing").Error; err != nil {
		t.Fatal(err)
	}
	msg.RawMessage = "Subject: not a MIME message\r\n\r\nnothing"
	r := httptest.NewRequest("POST", "/forward", nil)
	parts, err := h.readForwardParts(r, msg)
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range parts {
		if part.Content != "" || part.Ref == "" {
			t.Fatalf("a part was read into memory: %+v", part)
		}
	}
	req := sendMessageRequest{From: fx.mailbox.Address, To: []string{"target@remote.test"}, Subject: "Fwd: parts", BodyText: "fwd"}
	sent := &models.Message{MailboxID: fx.mailbox.ID, Folder: "Sent", MsgID: "<fwd-parts@delq.test>", Sender: fx.mailbox.Address, Subject: req.Subject, BodyText: req.BodyText, SizeBytes: len(req.Subject) + len(req.BodyText), IsRead: true, ReceivedAt: time.Now()}
	if err := spoolForwardForTest(h, r, req, sent, parts); err != nil {
		t.Fatalf("forward: %v", err)
	}
	parsed, err := rmime.Parse([]byte(composedRaw(t, h, sent.SourceRef)))
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Inline) != 1 || parsed.Inline[0].ContentID != "<logo@remote.test>" || len(parsed.Attachments) != 3 {
		t.Fatalf("forwarded parts: inline=%+v attachments=%+v", parsed.Inline, parsed.Attachments)
	}
	empties := 0
	for _, att := range parsed.Attachments {
		if att.Filename == "empty.txt" && att.Size == 0 {
			empties++
		}
	}
	if empties != 1 {
		t.Fatalf("the empty part was not forwarded: %+v", parsed.Attachments)
	}
	var copies []models.Attachment
	if err := tx.Where("message_id = ?", sent.ID).Order("id").Find(&copies).Error; err != nil || len(copies) != 4 {
		t.Fatalf("Sent copy rows: %+v %v", copies, err)
	}
	for i, row := range copies {
		if row.Disposition != rows[i].Disposition || row.ContentID != rows[i].ContentID || row.StorageRef == rows[i].StorageRef {
			t.Fatalf("Sent copy row %d: %+v, from %+v", i, row, rows[i])
		}
	}

	// A missing object is the forward's attachment error, as it was.
	if err := store.Delete(t.Context(), rows[1].StorageRef); err != nil {
		t.Fatal(err)
	}
	if _, err := h.readForwardParts(r, msg); err != errForwardAttachment {
		t.Fatalf("missing object: %v", err)
	}
}

// Reading a forward's parts streams each through a fixed buffer: a 32 MiB attachment
// costs far less than 32 MiB (#2107).
func TestReadForwardPartsDoesNotHoldA32MiBAttachment(t *testing.T) {
	if os.Getenv("DB_HOST") == "" {
		t.Skip("needs DB_HOST")
	}
	db := openScratchDB(t, "forwardalloc", []any{withForeignKeys, &models.Domain{}, &models.Mailbox{}, &models.Message{}, &models.Attachment{}})
	db.Logger = logger.Default.LogMode(logger.Silent)
	domain := models.Domain{Name: "alloc.example.test"}
	if err := db.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	box := models.Mailbox{DomainID: domain.ID, LocalPart: "a", Address: "a@" + domain.Name, Active: true}
	if err := db.Create(&box).Error; err != nil {
		t.Fatal(err)
	}
	h := NewMessageHandler(db, nil, nil, "", nil)
	h.forwardRoot = t.TempDir()
	const size = 32 << 20
	h.SetForwardStorage(size+1<<20, nil)
	source := io.LimitReader(repeatingReader("attachment octets "), size)
	ref, sum, n, err := attachmentstore.PutFilesystemReader(t.Context(), h.forwardRoot, source)
	if err != nil || n != size {
		t.Fatal(n, err)
	}
	original := models.Message{MailboxID: box.ID, Sender: "old@example.test", Subject: "big", HasAttachments: true, MsgID: "<big@example.test>"}
	if err := db.Create(&original).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.Attachment{MessageID: original.ID, Filename: "big.bin", ContentType: "application/octet-stream", SizeBytes: size, StorageType: "filesystem", StorageRef: ref, Checksum: sum}).Error; err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/forward", nil)
	if _, err := h.readForwardParts(r, &original); err != nil { // warm the connection and statement
		t.Fatal(err)
	}
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	parts, err := h.readForwardParts(r, &original)
	runtime.ReadMemStats(&after)
	if err != nil || len(parts) != 1 || parts[0].Size != size || parts[0].Checksum != sum {
		t.Fatalf("parts: %+v %v", parts, err)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 2<<20 {
		t.Fatalf("reading a 32 MiB part allocated %d bytes", allocated)
	}
}

// An imported message's rows keep each part's disposition and Content-ID, and its empty
// part (#2107).
func TestImportedPartsKeepTheirDispositionAndContentID(t *testing.T) {
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
	body := fmt.Sprintf(`{"raw_message":"%s","internal_date":"2020-01-02T03:04:05Z"}`, base64.StdEncoding.EncodeToString([]byte(importedMultipart)))
	r := authzRequest(http.MethodPost, fmt.Sprintf("/api/v1/accounts/%d/messages/import", fx.user.ID), fx.user.ID, nil, body)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	importMessageRoutes(h).ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("import: %d %s", w.Code, w.Body)
	}
	var rows []models.Attachment
	if err := db.Joins("JOIN messages ON messages.id = attachments.message_id").Where("messages.mailbox_id = ?", fx.user.ID).Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, row := range rows {
		got[row.Filename] = row.Disposition + " " + row.ContentID + " " + strconv.FormatInt(row.SizeBytes, 10)
	}
	want := map[string]string{"pic.png": "inline <pic> 7", "name.pdf": "attachment  4", "a b.bin": "attachment  8", "empty.bin": "attachment  0"}
	for name, w := range want {
		if got[name] != w {
			t.Fatalf("imported rows %v: %s is %q, want %q", got, name, got[name], w)
		}
	}
}

// partProbe is an outbound filter that reads what the send hands it, as the filters that
// send a message on do.
type partProbe struct{ read map[string][]byte }

func (p *partProbe) Name() string              { return "part_probe" }
func (p *partProbe) Type() pipeline.FilterType { return pipeline.FilterTypeAction }
func (p *partProbe) Execute(ctx context.Context, email *pipeline.EmailJSON) (*pipeline.FilterResult, error) {
	open := pipeline.AttachmentOpenerFromContext(ctx)
	for _, att := range append(append([]pipeline.Attachment(nil), email.Attachments...), email.Inline...) {
		if open == nil || !open.Holds(att) {
			continue
		}
		r, err := open.Open(ctx, att)
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(r)
		_ = r.Close()
		if err != nil {
			return nil, err
		}
		p.read[att.Filename] = data
	}
	return &pipeline.FilterResult{Type: pipeline.FilterTypeAction, Action: pipeline.ActionContinue}, nil
}

// A forward hands its parts to the outbound filters by reference, and they read them from
// where they are stored (#2107).
func TestOutboundFiltersReadTheForwardedParts(t *testing.T) {
	db, h, _, child := layeredSendFixture(t)
	h.layeredLimits = nil
	h.SetForwardStorage(1<<20, nil)
	probe := &partProbe{read: map[string][]byte{}}
	registry := pipeline.NewRegistry()
	registry.Register("part_probe", func([]byte) (pipeline.Filter, error) { return probe, nil })
	h.engine = pipeline.NewEngine(registry, nil)
	if err := db.Create(&models.Pipeline{DomainID: child.DomainID, Direction: "outbound", Active: true, Filters: json.RawMessage(`[{"name":"part_probe","type":"action","enabled":true,"config":{}}]`)}).Error; err != nil {
		t.Fatal(err)
	}
	original := models.Message{MailboxID: child.ID, Folder: "INBOX", Sender: "old@remote.test", Subject: "probe", HasAttachments: true, MsgID: "<probe@remote.test>"}
	if err := db.Create(&original).Error; err != nil {
		t.Fatal(err)
	}
	data := []byte("forwarded part")
	ref, sum, err := attachmentstore.PutFilesystem(t.Context(), h.forwardRoot, data)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.Attachment{MessageID: original.ID, Filename: "part.bin", ContentType: "application/octet-stream", SizeBytes: int64(len(data)), StorageType: "filesystem", StorageRef: ref, Checksum: sum}).Error; err != nil {
		t.Fatal(err)
	}
	claims := &auth.Claims{AccountID: child.ID, Address: child.Address, CredentialKind: models.CredentialKindAppPassword}
	parts, err := h.readForwardParts(limitRequest(claims), &original)
	if err != nil {
		t.Fatal(err)
	}
	req := sendMessageRequest{From: child.Address, To: []string{"far@remote.test"}, Subject: "Fwd: probe", BodyText: "body"}
	if out, err := h.sendMessageWithParts(limitRequest(claims), req, parts); err != nil || out.Code != 201 {
		t.Fatalf("forward: %+v %v", out, err)
	}
	if !bytes.Equal(probe.read["part.bin"], data) {
		t.Fatalf("the filter read %q", probe.read["part.bin"])
	}
}

type repeatingReader string

func (s repeatingReader) Read(p []byte) (int, error) {
	n := 0
	for n < len(p) {
		n += copy(p[n:], s)
	}
	return n, nil
}

// A held forward keeps references to copies of its parts, not their bytes; released after
// the message it forwards is deleted, objects and all, it still sends every part whole.
func TestHeldForwardReleasedAfterTheOriginalIsDeleted(t *testing.T) {
	db, h, owner, child := layeredSendFixture(t)
	h.SetForwardStorage(1<<20, nil)
	machine := &auth.Claims{AccountID: child.ID, Address: child.Address, CredentialKind: models.CredentialKindAppPassword}
	req := sendMessageRequest{From: child.Address, To: []string{"far@remote.test"}, Subject: "Fwd: held", BodyText: "body"}
	if out, err := h.sendMessage(limitRequest(machine), req); err != nil || out.Code != 201 {
		t.Fatalf("first send=%+v %v", out, err)
	}
	original := models.Message{MailboxID: child.ID, Folder: "INBOX", Sender: "old@remote.test", Subject: "held", HasAttachments: true, MsgID: "<held@remote.test>"}
	if err := db.Create(&original).Error; err != nil {
		t.Fatal(err)
	}
	contents := map[string][]byte{"logo.png": []byte("image"), "report.bin": []byte("report\x00\xff"), "empty.txt": nil}
	for _, row := range []models.Attachment{
		{Filename: "logo.png", ContentType: "image/png", Disposition: "inline", ContentID: "logo@remote.test"},
		{Filename: "report.bin", ContentType: "application/octet-stream", Disposition: "attachment"},
		{Filename: "empty.txt", ContentType: "text/plain", Disposition: "attachment"},
	} {
		data := contents[row.Filename]
		ref, sum, err := attachmentstore.PutFilesystem(t.Context(), h.forwardRoot, data)
		if err != nil {
			t.Fatal(err)
		}
		row.MessageID, row.SizeBytes, row.StorageType, row.StorageRef, row.Checksum = original.ID, int64(len(data)), "filesystem", ref, sum
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	parts, err := h.readForwardParts(limitRequest(machine), &original)
	if err != nil {
		t.Fatal(err)
	}
	out, err := h.sendMessageWithParts(limitRequest(machine), req, parts)
	if err != nil || out.Code != 202 {
		t.Fatalf("over limit=%+v %v; want 202 held", out, err)
	}
	held := out.Value.(ScheduledMessage)
	var stored models.ScheduledSend
	if err := db.First(&stored, held.ID).Error; err != nil {
		t.Fatal(err)
	}
	var retained []pipeline.Attachment
	if err := json.Unmarshal(stored.ForwardParts, &retained); err != nil || len(retained) != 3 {
		t.Fatalf("held parts: %s %v", stored.ForwardParts, err)
	}
	for i, part := range retained {
		if part.Content != "" || part.Ref == "" || part.Ref == parts[i].Ref || !strings.Contains(part.Ref, "copies") {
			t.Fatalf("held part %d keeps bytes or the original's object: %+v", i, part)
		}
	}
	if bytes.Contains(stored.ForwardParts, []byte("cmVwb3J0")) { // base64 of "report"
		t.Fatal("the held row keeps a part's bytes")
	}

	// The original goes, rows and objects.
	if err := db.Where("message_id = ?", original.ID).Delete(&models.Attachment{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Delete(&models.Message{}, original.ID).Error; err != nil {
		t.Fatal(err)
	}
	for _, part := range parts {
		if err := os.Remove(part.Ref); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}

	human := &auth.Claims{AccountID: owner.ID, Address: owner.Address, TwoFactor: true, SessionID: "person-session"}
	if out, err := h.releaseHeldSend(limitRequest(human), scheduledPath{ID: uint32(held.ID)}); err != nil || out.Code != 200 {
		t.Fatalf("release=%+v %v", out, err)
	}
	if err := db.First(&stored, held.ID).Error; err != nil || stored.Status != "sent" || stored.SentMessageID == nil {
		t.Fatalf("held state: %+v %v", stored, err)
	}
	var sent models.Message
	if err := db.First(&sent, *stored.SentMessageID).Error; err != nil {
		t.Fatal(err)
	}
	parsed, err := rmime.Parse([]byte(composedRaw(t, h, sent.SourceRef)))
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Inline) != 1 || parsed.Inline[0].ContentID != "logo@remote.test" || len(parsed.Attachments) != 2 {
		t.Fatalf("released parts: inline=%+v attachments=%+v", parsed.Inline, parsed.Attachments)
	}
	for _, att := range append(parsed.Inline, parsed.Attachments...) {
		data, _ := base64.StdEncoding.DecodeString(att.Content)
		if !bytes.Equal(data, contents[att.Filename]) {
			t.Fatalf("%s changed: %q", att.Filename, data)
		}
	}
}
