package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/messagestore"
	"github.com/rest-mail/rest-mail-server/pkg/apitypes"
	"gorm.io/gorm"
)

func referencedMessageConfig(t *testing.T) *config.Config {
	return &config.Config{
		Environment: "test", SMTPMaxMessageSize: config.DefaultSMTPMaxMessageSize,
		Attachments: config.AttachmentStorageConfig{Type: "filesystem"},
		Messages:    config.MessageStorageConfig{Directory: t.TempDir(), SpoolDir: t.TempDir()},
	}
}

func seedReferencedMessage(t *testing.T, f *credentialFixture, raw string) (*messagestore.Sources, models.Message) {
	t.Helper()
	store, err := messagestore.NewSources(f.db, f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	head, _, ok := strings.Cut(raw, "\r\n\r\n")
	if !ok {
		t.Fatal("fixture requires a header section")
	}
	object, err := store.Stage(t.Context(), strings.NewReader(raw), int64(len(raw)), []byte(head+"\r\n\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	message := models.Message{
		MailboxID: f.mailboxID, Folder: "INBOX", Sender: "sender@example.test", Subject: "gazette",
		ThreadID: "gazette", SourceRef: &object.Ref, SizeBytes: len(raw),
	}
	err = f.db.Transaction(func(tx *gorm.DB) error {
		if err := store.Publish(t.Context(), tx, object.Ref, 1); err != nil {
			return err
		}
		return store.CreateMessage(t.Context(), tx, &message)
	})
	if err != nil {
		t.Fatal(err)
	}
	return store, message
}

func TestReferencedRawHeadersAndSeal(t *testing.T) {
	useRealGrants(t)
	f := newCredentialFixtureWith(t, referencedMessageConfig(t))
	const raw = "Authentication-Results: restmail; dkim=pass\r\nFrom: sender@example.test\r\n" +
		"Subject: gazette\r\nList-Id: <gazette.example.test>\r\n" +
		"List-Unsubscribe: <https://example.test/u>\r\n\r\nbody with binary \xff\x00\r\n"
	_, message := seedReferencedMessage(t, f, raw)
	inline := message
	inline.ID, inline.SourceRef, inline.RawMessage = 0, nil, raw
	inline.Folder, inline.ThreadID = "Archive", "inline-gazette"
	if err := f.db.Create(&inline).Error; err != nil {
		t.Fatal(err)
	}
	if inline.SealedHash == "" || inline.SealedHash != message.SealedHash {
		t.Fatalf("inline seal %q, referenced seal %q", inline.SealedHash, message.SealedHash)
	}
	for _, id := range []uint{message.ID, inline.ID} {
		rr := f.do(http.MethodGet, fmt.Sprintf("/api/v1/messages/%d/raw", id), f.session, "")
		if rr.Code != http.StatusOK || rr.Body.String() != raw || rr.Header().Get("Content-Length") != strconv.Itoa(len(raw)) {
			t.Fatalf("raw %d: HTTP %d length %q body %q", id, rr.Code, rr.Header().Get("Content-Length"), rr.Body.String())
		}
	}
	path := fmt.Sprintf("/api/v1/messages/%d", message.ID)
	account := fmt.Sprintf("/api/v1/accounts/%d", f.mailboxID)
	for _, route := range []string{path, account + "/search?q=gazette&folder=INBOX", account + "/folders/INBOX/messages", account + "/threads/gazette"} {
		rr := f.do(http.MethodGet, route, f.session, "")
		if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"class":"newsletter"`) ||
			!strings.Contains(rr.Body.String(), `"auth_verdict":"signed"`) {
			t.Fatalf("%s did not use the stored header: HTTP %d %s", route, rr.Code, rr.Body)
		}
	}
	if rr := f.do(http.MethodGet, path+"/unsubscribe", f.session, ""); rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "https://example.test/u") {
		t.Fatalf("unsubscribe headers: HTTP %d %s", rr.Code, rr.Body)
	}
	assertSeal := func(want, computed string) {
		t.Helper()
		rr := f.do(http.MethodGet, path+"/seal", f.session, "")
		var result struct {
			Data struct {
				State        string `json:"state"`
				SealedHash   string `json:"sealed_hash"`
				ComputedHash string `json:"computed_hash"`
			}
		}
		if rr.Code != http.StatusOK || json.Unmarshal(rr.Body.Bytes(), &result) != nil ||
			result.Data.State != want || result.Data.SealedHash != message.SealedHash || result.Data.ComputedHash != computed {
			t.Fatalf("want seal %s: HTTP %d %s", want, rr.Code, rr.Body)
		}
	}
	assertSeal("intact", message.SealedHash)
	changed := strings.Replace(raw, "body with binary", "Body with binary", 1)
	if err := os.WriteFile(filepath.Join(f.cfg.Messages.Directory, *message.SourceRef), []byte(changed), 0o600); err != nil {
		t.Fatal(err)
	}
	recomputed, err := message.SealDigestFrom(strings.NewReader(changed))
	if err != nil {
		t.Fatal(err)
	}
	assertSeal("mismatch", recomputed)
}

func TestReferencedMetadataMatchesProtocolOctets(t *testing.T) {
	useRealGrants(t)
	s := newEightBitServerWith(t, referencedMessageConfig(t))
	password := s.createAppPassword("referenced-source").Password
	for _, shape := range metadataShapes() {
		t.Run(shape.name, func(t *testing.T) {
			s.t = t
			_, message := seedReferencedMessage(t, s.credentialFixture, shape.message)
			if message.RawSize != len(shape.message) {
				t.Fatalf("raw size %d, want %d", message.RawSize, len(shape.message))
			}
			for _, utf8 := range []bool{false, true} {
				client := newIMAPReader(t, s, password, utf8)
				whole := client.fetchBody("BODY.PEEK[]")
				if size := client.fetchNumber("RFC822.SIZE"); size != len(whole) {
					t.Fatalf("IMAP UTF8=%v: metadata %d, sent %d", utf8, size, len(whole))
				}
			}
			listed, retrieved, _ := s.pop3Measure(password)
			if listed != len(retrieved) {
				t.Fatalf("POP3 metadata %d, sent %d", listed, len(retrieved))
			}
		})
	}
}

func TestReferencedCopyMoveAndDelete(t *testing.T) {
	useRealGrants(t)
	f := newCredentialFixtureWith(t, referencedMessageConfig(t))
	const raw = "From: sender@example.test\r\nSubject: gazette\r\n\r\nbody\r\n"
	store, original := seedReferencedMessage(t, f, raw)
	if err := f.db.Create(&models.QuotaUsage{MailboxID: f.mailboxID}).Error; err != nil {
		t.Fatal(err)
	}
	assertOwners := func(want int64) {
		t.Helper()
		var row models.MessageSource
		if err := f.db.Take(&row, "ref = ?", *original.SourceRef).Error; err != nil {
			t.Fatal(err)
		}
		if row.ReferenceCount != want || (row.ReapAt != nil) != (want == 0) {
			t.Fatalf("source has %d owners and reap_at %v, want %d owners", row.ReferenceCount, row.ReapAt, want)
		}
	}
	transfer := func(id uint, folder, to string, move bool) uint {
		t.Helper()
		body, err := json.Marshal(apitypes.TransferMessagesBody{SourceFolder: folder, Folder: to, MessageIDs: []uint{id}, Move: move})
		if err != nil {
			t.Fatal(err)
		}
		rr := f.do(http.MethodPost, fmt.Sprintf("/api/v1/accounts/%d/messages/transfer", f.mailboxID), f.session, string(body))
		var result struct {
			Data apitypes.TransferMessagesResult
		}
		if rr.Code != http.StatusOK || json.Unmarshal(rr.Body.Bytes(), &result) != nil || len(result.Data.IDs) != 1 {
			t.Fatalf("transfer: HTTP %d %s", rr.Code, rr.Body)
		}
		destination := result.Data.IDs[0]
		var row models.Message
		if err := f.db.First(&row, destination).Error; err != nil {
			t.Fatal(err)
		}
		if row.SourceRef == nil || *row.SourceRef != *original.SourceRef || len(row.Source()) != 0 ||
			row.SealedHash != original.SealedHash || row.RawSize != len(raw) {
			t.Fatalf("transfer did not preserve the source reference and metadata: %+v", row)
		}
		return destination
	}
	remove := func(id uint) {
		t.Helper()
		rr := f.do(http.MethodDelete, fmt.Sprintf("/api/v1/messages/%d?permanent=true", id), f.session, "")
		if rr.Code < 200 || rr.Code >= 300 {
			t.Fatalf("delete: HTTP %d %s", rr.Code, rr.Body)
		}
	}
	copyID := transfer(original.ID, "INBOX", "Archive", false)
	assertOwners(2)
	moveID := transfer(copyID, "Archive", "Sent", true)
	assertOwners(2)
	remove(original.ID)
	assertOwners(1)
	if reaped, err := store.Reap(t.Context()); err != nil || reaped != 0 {
		t.Fatalf("reaped live source: %d %v", reaped, err)
	}
	rr := f.do(http.MethodGet, fmt.Sprintf("/api/v1/messages/%d/raw", moveID), f.session, "")
	if rr.Code != http.StatusOK || rr.Body.String() != raw {
		t.Fatalf("surviving raw: HTTP %d %q", rr.Code, rr.Body.Bytes())
	}
	remove(moveID)
	assertOwners(0)
	if reaped, err := store.Reap(t.Context()); err != nil || reaped != 1 {
		t.Fatalf("released source: reaped %d, error %v", reaped, err)
	}
}
