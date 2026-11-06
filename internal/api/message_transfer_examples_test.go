package api

import (
	"encoding/json"
	"net/http"
	"os"
	"slices"
	"testing"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/examplepairs"
)

// transferExamples are the transferMessages pairs, each sent through the
// production router to a scratch PostgreSQL database holding the signed-in
// account's messages 1 and 2 in INBOX (go-imap#161).
var transferExamples = []string{"copied", "moved", "missing", "mailbox_full", "body_array", "no_messages", "invalid_id"}

// transferRecordEnv, when set, names a file the test writes the answers it got
// to, as pairs, instead of checking them: how the canonical pairs were made.
const transferRecordEnv = "RESTMAIL_TRANSFER_RECORD"

func TestMessageTransferNamedExamples(t *testing.T) {
	if os.Getenv("DB_HOST") == "" {
		t.Skip("set DB_HOST for the PostgreSQL production-router transfer proofs")
	}
	useRealGrants(t)
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	recorded := oapifly.ExampleFile{"transferMessages": {}}
	for _, name := range transferExamples {
		t.Run(name, func(t *testing.T) {
			pair, ok := pairs["transferMessages"][name]
			if os.Getenv(transferRecordEnv) == "" {
				examplepairs.Proven(t, "transferMessages", name)
				if !ok || len(pair.Response.Body) == 0 {
					t.Fatal("the named request/response pair is incomplete")
				}
			}
			f := newCredentialFixtureWith(t, &config.Config{Environment: "test"})
			if f.mailboxID != 1 {
				t.Fatalf("the fixture account is %d; the pairs name account 1", f.mailboxID)
			}
			if err := f.db.Create(&models.QuotaUsage{MailboxID: f.mailboxID}).Error; err != nil {
				t.Fatal(err)
			}
			for _, id := range []uint{1, 2} {
				if err := f.db.Create(&models.Message{ID: id, MailboxID: f.mailboxID, Folder: "INBOX", Sender: "old@example.test",
					Subject: "kept", BodyText: "body", RawMessage: "Subject: kept\r\n\r\nbody\r\n", SizeBytes: 8}).Error; err != nil {
					t.Fatal(err)
				}
			}
			// New messages are numbered from 41, so a copy's ID is plainly new.
			if err := f.db.Exec("SELECT setval('messages_id_seq', 40)").Error; err != nil {
				t.Fatal(err)
			}
			if name == "mailbox_full" {
				if err := f.db.Model(&models.Mailbox{}).Where("id = ?", f.mailboxID).
					Updates(map[string]any{"quota_bytes": 10, "quota_used_bytes": 10}).Error; err != nil {
					t.Fatal(err)
				}
			}
			body := `{"source_folder":"INBOX","message_ids":[2,1],"folder":"Archive"}`
			switch name {
			case "moved":
				body = `{"source_folder":"INBOX","message_ids":[2,1],"folder":"Archive","move":true}`
			case "missing":
				body = `{"source_folder":"INBOX","message_ids":[2,2147483647],"folder":"Archive"}`
			case "body_array":
				body = `[]`
			case "no_messages":
				body = `{"source_folder":"INBOX","message_ids":[],"folder":"Archive"}`
			}
			id := "1"
			if name == "invalid_id" {
				id = "0"
			}
			rr := f.do(http.MethodPost, "/api/v1/accounts/"+id+"/messages/transfer", f.session, body)

			var inbox, archive []uint
			if err := f.db.Model(&models.Message{}).Where("folder = ?", "INBOX").Order("id").Pluck("id", &inbox).Error; err != nil {
				t.Fatal(err)
			}
			if err := f.db.Model(&models.Message{}).Where("folder = ?", "Archive").Order("id").Pluck("id", &archive).Error; err != nil {
				t.Fatal(err)
			}
			switch {
			case name == "copied" && (!slices.Equal(inbox, []uint{1, 2}) || !slices.Equal(archive, []uint{41, 42})):
				t.Fatalf("copy left INBOX %v, Archive %v", inbox, archive)
			case name == "moved" && (len(inbox) != 0 || !slices.Equal(archive, []uint{41, 42})):
				t.Fatalf("move left INBOX %v, Archive %v", inbox, archive)
			case rr.Code >= 400 && (!slices.Equal(inbox, []uint{1, 2}) || len(archive) != 0):
				t.Fatalf("refused transfer left INBOX %v, Archive %v", inbox, archive)
			}

			if path := os.Getenv(transferRecordEnv); path != "" {
				var requestBody json.RawMessage = []byte(body)
				recorded["transferMessages"][name] = oapifly.ExamplePair{
					Request:  oapifly.ExampleRequest{Path: map[string]any{"id": json.Number(id)}, Body: requestBody, MediaType: "application/json", Invalid: name == "body_array" || name == "no_messages" || name == "invalid_id"},
					Response: oapifly.ExampleResponse{Status: rr.Code, Body: append(json.RawMessage(nil), rr.Body.Bytes()...), MediaType: "application/json"},
				}
				return
			}
			if rr.Code != pair.Response.Status || rr.Header().Get("Content-Type") != pair.Response.MediaType {
				t.Fatalf("%s = %d %s; want %d %s", name, rr.Code, rr.Body, pair.Response.Status, pair.Response.MediaType)
			}
			var sent, want any
			if err := json.Unmarshal(pair.Request.Body, &want); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(body), &sent); err != nil {
				t.Fatal(err)
			}
			if got, _ := json.Marshal(sent); string(got) != string(mustImportJSON(t, want)) || string(mustImportJSON(t, pair.Request.Path["id"])) != id {
				t.Fatalf("the pair's request differs from the one sent: %s %v", pair.Request.Body, pair.Request.Path)
			}
			assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
		})
	}
	if path := os.Getenv(transferRecordEnv); path != "" {
		data, err := oapifly.MarshalExamples(recorded)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
