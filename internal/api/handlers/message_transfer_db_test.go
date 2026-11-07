package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	apitypes "github.com/rest-mail/rest-mail-server/internal/api/types"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"gorm.io/gorm"
)

func transferRoutes(h *MessageTransferHandler) http.Handler {
	r := chi.NewRouter()
	h.Register(endpoint.Routes{Router: r, Registry: endpoint.NewRegistry()})
	return r
}

// transferFixture is an account with four INBOX messages, the last two with an
// attachment, and an empty Archive.
type transferFixture struct {
	db      *gorm.DB
	account uint
	caller  uint
	ids     []uint
}

func newTransferFixture(t *testing.T) transferFixture {
	t.Helper()
	db := importProofDB(t)
	fx := seedAccountsFixture(t, db)
	if err := db.Create(&models.QuotaUsage{MailboxID: fx.user.ID}).Error; err != nil {
		t.Fatal(err)
	}
	var ids []uint
	for i := range 4 {
		msg := models.Message{MailboxID: fx.user.ID, Folder: "INBOX", MsgID: fmt.Sprintf("<m%d@example.test>", i),
			Sender: "old@example.test", Subject: fmt.Sprintf("message %d", i), BodyText: "body",
			RawMessage: fmt.Sprintf("Message-ID: <m%d@example.test>\r\nSubject: message %d\r\n\r\nbody\r\n", i, i),
			IsRead:     true, Keywords: []string{"$Forwarded"}, ReceivedAt: time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC),
			SizeBytes: 20, HasAttachments: i >= 2}
		if err := db.Create(&msg).Error; err != nil {
			t.Fatal(err)
		}
		if msg.HasAttachments {
			if err := db.Create(&models.Attachment{MessageID: msg.ID, Filename: "a.txt", SizeBytes: 5, StorageRef: "ref", StorageType: "filesystem"}).Error; err != nil {
				t.Fatal(err)
			}
		}
		ids = append(ids, msg.ID)
	}
	return transferFixture{db: db, account: fx.user.ID, caller: fx.user.ID, ids: ids}
}

func (fx transferFixture) call(t *testing.T, h *MessageTransferHandler, body apitypes.TransferMessagesBody) *httptest.ResponseRecorder {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request := authzRequest(http.MethodPost, fmt.Sprintf("/api/v1/accounts/%d/messages/transfer", fx.account), fx.caller, nil, string(encoded))
	request.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	transferRoutes(h).ServeHTTP(w, request)
	return w
}

// state is every message's ID by folder, the attachment count, the account's
// quota use and the audit entries for transfers.
type transferState struct {
	folders            map[string][]uint
	attachments, audit int64
	quota              int64
	counted            int64
}

func (fx transferFixture) state(t *testing.T) transferState {
	t.Helper()
	var messages []models.Message
	if err := fx.db.Where("mailbox_id = ?", fx.account).Order("id").Find(&messages).Error; err != nil {
		t.Fatal(err)
	}
	s := transferState{folders: map[string][]uint{}}
	for _, m := range messages {
		s.folders[m.Folder] = append(s.folders[m.Folder], m.ID)
	}
	if err := fx.db.Model(&models.Attachment{}).Count(&s.attachments).Error; err != nil {
		t.Fatal(err)
	}
	if err := fx.db.Table("audit_log").Where("action = ?", "messages.transfer").Count(&s.audit).Error; err != nil {
		t.Fatal(err)
	}
	var account models.Mailbox
	if err := fx.db.First(&account, fx.account).Error; err != nil {
		t.Fatal(err)
	}
	s.quota = account.QuotaUsedBytes
	var usage models.QuotaUsage
	if err := fx.db.Where("mailbox_id = ?", fx.account).First(&usage).Error; err != nil {
		t.Fatal(err)
	}
	s.counted = int64(usage.MessageCount)
	return s
}

func requireSameTransferState(t *testing.T, got, want transferState) {
	t.Helper()
	if len(got.folders) != len(want.folders) || got.attachments != want.attachments || got.audit != want.audit ||
		got.quota != want.quota || got.counted != want.counted {
		t.Fatalf("state changed: got %+v, want %+v", got, want)
	}
	for folder, ids := range want.folders {
		if !slices.Equal(got.folders[folder], ids) {
			t.Fatalf("%s holds %v, want %v", folder, got.folders[folder], ids)
		}
	}
}

// A copy or move that fails on its 3rd message rolls back the two before it:
// the destination, the source, the attachments, quota and audit are as they
// were (RFC 3501 §6.4.7, RFC 6851 §3.3).
func TestMessageTransferPostgresRollsBackOnALaterRow(t *testing.T) {
	for _, move := range []bool{false, true} {
		t.Run(fmt.Sprintf("move=%v", move), func(t *testing.T) {
			fx := newTransferFixture(t)
			h := NewMessageTransferHandler(fx.db, nil)
			h.rowFault = func(n int) error {
				if n == 3 {
					return errors.New("injected row failure")
				}
				return nil
			}
			before := fx.state(t)
			w := fx.call(t, h, apitypes.TransferMessagesBody{SourceFolder: "INBOX", MessageIDs: fx.ids, Folder: "Archive", Move: move})
			if w.Code != http.StatusInternalServerError {
				t.Fatalf("faulted transfer: %d %s", w.Code, w.Body)
			}
			requireSameTransferState(t, fx.state(t), before)
		})
	}
}

// A message that is not in the source folder refuses the whole set with 404,
// even when it is named last.
func TestMessageTransferPostgresRefusesTheSetForOneMissingMessage(t *testing.T) {
	fx := newTransferFixture(t)
	h := NewMessageTransferHandler(fx.db, nil)
	before := fx.state(t)
	w := fx.call(t, h, apitypes.TransferMessagesBody{SourceFolder: "INBOX", MessageIDs: append(slices.Clone(fx.ids), fx.ids[3]+1000), Folder: "Archive"})
	if w.Code != http.StatusNotFound {
		t.Fatalf("missing message: %d %s", w.Code, w.Body)
	}
	requireSameTransferState(t, fx.state(t), before)
}

// A copy makes new, higher IDs in order with flags, keywords, date, source and
// attachments kept, charges quota and leaves the originals; a move leaves no
// original and charges nothing.
func TestMessageTransferPostgresCopiesAndMoves(t *testing.T) {
	fx := newTransferFixture(t)
	h := NewMessageTransferHandler(fx.db, nil)
	before := fx.state(t)
	order := []uint{fx.ids[3], fx.ids[0], fx.ids[2]}
	w := fx.call(t, h, apitypes.TransferMessagesBody{SourceFolder: "inbox", MessageIDs: order, Folder: "Archive"})
	if w.Code != http.StatusOK {
		t.Fatalf("copy: %d %s", w.Code, w.Body)
	}
	var copied endpoint.Data[apitypes.TransferMessagesResult]
	if err := json.Unmarshal(w.Body.Bytes(), &copied); err != nil {
		t.Fatal(err)
	}
	after := fx.state(t)
	if !slices.Equal(after.folders["Archive"], copied.Data.IDs) || len(copied.Data.IDs) != 3 || copied.Data.IDs[0] <= fx.ids[3] ||
		!slices.IsSorted(copied.Data.IDs) || !slices.Equal(after.folders["INBOX"], fx.ids) {
		t.Fatalf("copy result %v, folders %v", copied.Data.IDs, after.folders)
	}
	if after.attachments != before.attachments+2 || after.quota != before.quota+3*20+2*5 || after.counted != before.counted+3 || after.audit != 1 {
		t.Fatalf("copy bookkeeping: before %+v after %+v", before, after)
	}
	var first, source models.Message
	if err := fx.db.First(&first, copied.Data.IDs[0]).Error; err != nil {
		t.Fatal(err)
	}
	if err := fx.db.First(&source, fx.ids[3]).Error; err != nil {
		t.Fatal(err)
	}
	if first.RawMessage != source.RawMessage || !first.IsRead || !slices.Equal(first.Keywords, source.Keywords) ||
		!first.ReceivedAt.Equal(source.ReceivedAt) || first.MsgID != source.MsgID || !first.HasAttachments {
		t.Fatalf("copy lost metadata: %+v", first)
	}

	w = fx.call(t, h, apitypes.TransferMessagesBody{SourceFolder: "INBOX", MessageIDs: []uint{fx.ids[2], fx.ids[1]}, Folder: "Trash", Move: true})
	if w.Code != http.StatusOK {
		t.Fatalf("move: %d %s", w.Code, w.Body)
	}
	var moved endpoint.Data[apitypes.TransferMessagesResult]
	if err := json.Unmarshal(w.Body.Bytes(), &moved); err != nil {
		t.Fatal(err)
	}
	final := fx.state(t)
	if !slices.Equal(final.folders["Trash"], moved.Data.IDs) || !slices.Equal(final.folders["INBOX"], []uint{fx.ids[0], fx.ids[3]}) ||
		moved.Data.IDs[0] <= copied.Data.IDs[2] {
		t.Fatalf("move result %v, folders %v", moved.Data.IDs, final.folders)
	}
	if final.attachments != after.attachments || final.quota != after.quota || final.counted != after.counted || final.audit != 2 {
		t.Fatalf("move bookkeeping: before %+v after %+v", after, final)
	}
	var att int64
	if err := fx.db.Model(&models.Attachment{}).Where("message_id = ?", moved.Data.IDs[0]).Count(&att).Error; err != nil || att != 1 {
		t.Fatalf("moved attachment: %d %v", att, err)
	}
}
