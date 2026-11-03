package api

import (
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/examplepairs"
)

const vanishedUIDsPath = "/api/v1/accounts/{id}/folders/{folder}/vanished"

// The mod-sequence pairs (RFC 7162, go-imap#162), sent through the production router to
// a scratch PostgreSQL database whose triggers keep the mod-sequences: a change made with
// unchanged_since after another change is refused, and the UIDs that left a folder are
// listed above a mod-sequence. Only the allocated account id is replaced with the
// published one.
func TestCondstoreStoredExamples(t *testing.T) {
	if os.Getenv("DB_HOST") == "" {
		t.Skip("set DB_HOST for PostgreSQL production-router mod-sequence proofs")
	}
	useRealGrants(t)
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	stamp := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	seed := func(f *credentialFixture, id uint) {
		t.Helper()
		m := models.Message{ID: id, MailboxID: f.mailboxID, Folder: "INBOX", MsgID: "<message@example.net>",
			Sender: "sender@example.net", SenderName: "Sender", RecipientsTo: models.JSONB(`["recipient@example.com"]`),
			RecipientsCc: models.JSONB(`[]`), Subject: "Subject", SizeBytes: 80, ReceivedAt: stamp, CreatedAt: stamp, UpdatedAt: stamp}
		if err := f.db.Create(&m).Error; err != nil {
			t.Fatal(err)
		}
	}

	t.Run("updateMessage/modified", func(t *testing.T) {
		examplepairs.Proven(t, "updateMessage", "modified")
		pair := pairs["updateMessage"]["modified"]
		f := newCredentialFixture(t)
		seed(f, 1)
		// Another client flags the message: its mod_seq rises from 2, the first in the folder, to 3.
		if err := f.db.Model(&models.Message{}).Where("id = ?", 1).Update("is_flagged", true).Error; err != nil {
			t.Fatal(err)
		}
		rr := f.do(http.MethodPatch, messageCollectionReadURI(messageUpdatePath, pair), f.session, string(pair.Request.Body))
		if rr.Code != pair.Response.Status || rr.Header().Get("Content-Type") != pair.Response.MediaType {
			t.Fatalf("got %d %s %s, want %d %s", rr.Code, rr.Header().Get("Content-Type"), rr.Body.String(), pair.Response.Status, pair.Response.MediaType)
		}
		assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
		var stored models.Message
		if err := f.db.First(&stored, 1).Error; err != nil {
			t.Fatal(err)
		}
		if stored.IsRead || stored.ModSeq != 3 {
			t.Fatalf("refused change wrote: is_read %t, mod_seq %d; want false, 3", stored.IsRead, stored.ModSeq)
		}

		// At the current mod_seq the same change is made and answered with the new one.
		rr = f.do(http.MethodPatch, "/api/v1/messages/1", f.session, `{"is_read":true,"unchanged_since":3}`)
		if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"mod_seq":4`) {
			t.Fatalf("change at the current mod_seq: %d %s", rr.Code, rr.Body.String())
		}
	})

	t.Run("listVanishedUIDs/found", func(t *testing.T) {
		examplepairs.Proven(t, "listVanishedUIDs", "found")
		pair := pairs["listVanishedUIDs"]["found"]
		f := newCredentialFixture(t)
		// UIDs 3, 5 and 7 each arrive in INBOX and leave it for good: the folder's
		// mod-sequence rises with every arrival and every departure.
		for _, id := range []uint{3, 5, 7} {
			seed(f, id)
			if err := f.db.Delete(&models.Message{}, id).Error; err != nil {
				t.Fatal(err)
			}
		}
		path := strings.Replace(vanishedUIDsPath, "{id}", strconv.FormatUint(uint64(f.accountID), 10), 1)
		inbox := strings.Replace(path, "{folder}", "INBOX", 1)
		rr := f.do(http.MethodGet, messageCollectionReadURI(path, pair), f.session, "")
		if rr.Code != pair.Response.Status || rr.Header().Get("Content-Type") != pair.Response.MediaType {
			t.Fatalf("got %d %s %s, want %d %s", rr.Code, rr.Header().Get("Content-Type"), rr.Body.String(), pair.Response.Status, pair.Response.MediaType)
		}
		assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())

		// The folder listing's HIGHESTMODSEQ counts the departures, though INBOX is empty.
		rr = f.do(http.MethodGet, "/api/v1/accounts/"+strconv.FormatUint(uint64(f.accountID), 10)+"/folders", f.session, "")
		if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"name":"INBOX","total":0,"unread":0,"uid_next":8,"highest_mod_seq":7`) {
			t.Fatalf("folder listing after the departures: %d %s", rr.Code, rr.Body.String())
		}

		// Pages follow after_uid, and a message that comes back is no longer listed.
		rr = f.do(http.MethodGet, inbox+"?limit=1", f.session, "")
		if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"data":[{"uid":3,"mod_seq":3}]`) || !strings.Contains(rr.Body.String(), `"cursor":"3","has_more":true`) {
			t.Fatalf("first page: %d %s", rr.Code, rr.Body.String())
		}
		seed(f, 5)
		rr = f.do(http.MethodGet, inbox+"?after_uid=3", f.session, "")
		if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"data":[{"uid":7,"mod_seq":7}]`) {
			t.Fatalf("after UID 3, with UID 5 back: %d %s", rr.Code, rr.Body.String())
		}
	})
}
