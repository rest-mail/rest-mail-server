package handlers

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/gateway/apiclient"
	gatewaysmtp "github.com/rest-mail/rest-mail-server/internal/gateway/smtp"
	"gorm.io/gorm"
)

// These tests are for issue #1707. A mail app that saves its own copy of what it sent
// uploads it to Sent with APPEND just after submission has saved one there. They run a
// real IMAP session and the real gateway against the real import on PostgreSQL, with
// the Sent copy made by the store call the SMTP session makes.

// sentCopyMessage is a message as a mail app uploads it.
func sentCopyMessage(messageID string) string {
	return "Message-ID: <" + messageID + ">\r\n" +
		"From: Sender <sender@example.test>\r\n" +
		"To: Someone <someone@example.net>\r\n" +
		"Subject: sent copy\r\n\r\nthe body\r\n"
}

// submitToSent saves the message as authenticated submission does, under its Message-ID
// without brackets as the session parses it. The source carries the Received field the
// session stamps, so its octets differ from what the app uploads, as they do for real.
func submitToSent(t *testing.T, db *gorm.DB, account models.Mailbox, messageID string) uint {
	t.Helper()
	ref, err := gatewaysmtp.NewStore(db, "").PersistSubmittedMessage(gatewaysmtp.SubmittedMessage{
		Sender:     account.Address,
		MessageID:  messageID,
		Subject:    "sent copy",
		BodyText:   "the body",
		RawMessage: "Received: from app by smtp.test\r\n" + sentCopyMessage(messageID),
	})
	if err != nil {
		t.Fatalf("PersistSubmittedMessage: %v", err)
	}
	if ref == nil {
		t.Fatal("submission saved nothing for a local sender")
	}
	return *ref
}

func folderMessages(t *testing.T, db *gorm.DB, account models.Mailbox, folder string) []models.Message {
	t.Helper()
	var stored []models.Message
	if err := db.Where("mailbox_id = ? AND folder = ?", account.ID, folder).Order("id").Find(&stored).Error; err != nil {
		t.Fatal(err)
	}
	return stored
}

func backdateSubmission(t *testing.T, db *gorm.DB, id uint, age time.Duration) {
	t.Helper()
	result := db.Exec("UPDATE messages SET submitted_at = ? WHERE id = ?", time.Now().Add(-age), id)
	if result.Error != nil {
		t.Fatal(result.Error)
	}
	if result.RowsAffected != 1 {
		t.Fatalf("backdated %d rows, want 1", result.RowsAffected)
	}
}

// The issue's sequence: submit, then APPEND the same Message-ID to Sent. The upload
// succeeds, Sent holds one message and APPENDUID names that message.
func TestIMAPAppendOfSubmittedMessageKeepsOneSentCopy(t *testing.T) {
	db := importProofDB(t)
	account := seedAccountsFixture(t, db).user
	apiURL := imapProofAPI(t, db, account)
	client := newIMAPProofClient(t, apiURL)

	const messageID = "one-copy@example.test"
	submitted := submitToSent(t, db, account, messageID)
	client.ok("SELECT Sent")

	untagged, uid := client.append("Sent", `(\Seen) `, sentCopyMessage(messageID))
	if uid != submitted {
		t.Errorf("APPENDUID = %d, want the submitted message %d (RFC 4315)", uid, submitted)
	}
	for _, line := range untagged {
		if strings.HasSuffix(line, "EXISTS") {
			t.Errorf("APPEND added nothing, so nothing should be announced: %q", line)
		}
	}

	stored := folderMessages(t, db, account, "Sent")
	if len(stored) != 1 {
		t.Fatalf("Sent holds %d messages, want one", len(stored))
	}
	if stored[0].ID != submitted || !strings.HasPrefix(stored[0].RawMessage, "Received: ") {
		t.Errorf("Sent holds message %d (source %.20q), want the submitted %d unchanged", stored[0].ID, stored[0].RawMessage, submitted)
	}

	// A second client, starting fresh, finds that one message under that UID.
	reader := newIMAPProofClient(t, apiURL)
	untagged, _ = reader.ok("SELECT Sent")
	requireLineWith(t, untagged, "* 1 EXISTS")
	if got := reader.flagsByUID(); len(got) != 1 {
		t.Errorf("a fresh session sees %v, want only message %d", got, submitted)
	} else if _, ok := got[submitted]; !ok {
		t.Errorf("a fresh session sees %v, want message %d", got, submitted)
	}
}

// Everything that should find the submitted message: the same upload with a date-time,
// which the gateway stores by another call, a repeat of the upload, and a Message-ID the
// session had kept in brackets. The owner's rule is about the message saved on
// submission, so while that message is just saved every upload of it finds it.
func TestIMAPAppendInsideTheWindowFindsTheSubmittedMessage(t *testing.T) {
	db := importProofDB(t)
	account := seedAccountsFixture(t, db).user
	client := newIMAPProofClient(t, imapProofAPI(t, db, account))

	check := func(t *testing.T, flags, messageID string, submitted uint) {
		t.Helper()
		if _, uid := client.append("Sent", flags, sentCopyMessage(messageID)); uid != submitted {
			t.Errorf("APPENDUID = %d, want the submitted message %d", uid, submitted)
		}
		var stored int64
		if err := db.Model(&models.Message{}).Where("mailbox_id = ? AND message_id IN ?", account.ID, []string{messageID, "<" + messageID + ">"}).Count(&stored).Error; err != nil {
			t.Fatal(err)
		}
		if stored != 1 {
			t.Errorf("the account holds %d messages with this Message-ID, want one", stored)
		}
	}

	t.Run("with a date-time", func(t *testing.T) {
		submitted := submitToSent(t, db, account, "dated@example.test")
		check(t, `(\Seen) "02-Jan-2020 03:04:05 +0000" `, "dated@example.test", submitted)
	})
	t.Run("uploaded twice", func(t *testing.T) {
		submitted := submitToSent(t, db, account, "twice@example.test")
		check(t, "", "twice@example.test", submitted)
		check(t, "", "twice@example.test", submitted)
	})
	t.Run("kept in brackets", func(t *testing.T) {
		submitted := submitToSent(t, db, account, "<bracketed@example.test>")
		check(t, "", "bracketed@example.test", submitted)
	})
	t.Run("near the end of the window", func(t *testing.T) {
		submitted := submitToSent(t, db, account, "late@example.test")
		backdateSubmission(t, db, submitted, 4*time.Minute)
		check(t, "", "late@example.test", submitted)
	})
}

// requireStored runs APPEND and requires a new message with a new UID.
func requireStored(t *testing.T, client *imapProofClient, db *gorm.DB, account models.Mailbox, folder, message string, before uint) uint {
	t.Helper()
	_, uid := client.append(folder, "", message)
	if uid <= before {
		t.Errorf("APPENDUID = %d, want a new message above %d", uid, before)
	}
	var row models.Message
	if err := db.Where("id = ? AND mailbox_id = ? AND folder = ?", uid, account.ID, folder).First(&row).Error; err != nil {
		t.Fatalf("no message %d in %s: %v", uid, folder, err)
	}
	if row.RawMessage != message {
		t.Errorf("message %d holds %q, want the uploaded source", uid, row.RawMessage)
	}
	return uid
}

// Each of these stores a message, as APPEND did before #1707.
func TestIMAPAppendThatDoesNotMatchASubmissionStoresAMessage(t *testing.T) {
	db := importProofDB(t)
	fx := seedAccountsFixture(t, db)
	account := fx.user
	client := newIMAPProofClient(t, imapProofAPI(t, db, account))

	sentRows := func(t *testing.T, messageID string, want int) {
		t.Helper()
		var stored int64
		if err := db.Model(&models.Message{}).Where("mailbox_id = ? AND folder = ? AND message_id IN ?", account.ID, "Sent", []string{messageID, "<" + messageID + ">"}).Count(&stored).Error; err != nil {
			t.Fatal(err)
		}
		if stored != int64(want) {
			t.Errorf("Sent holds %d messages with this Message-ID, want %d", stored, want)
		}
	}

	t.Run("outside the window", func(t *testing.T) {
		submitted := submitToSent(t, db, account, "old@example.test")
		backdateSubmission(t, db, submitted, 6*time.Minute)
		requireStored(t, client, db, account, "Sent", sentCopyMessage("old@example.test"), submitted)
		sentRows(t, "old@example.test", 2)
	})
	t.Run("to another folder", func(t *testing.T) {
		submitted := submitToSent(t, db, account, "elsewhere@example.test")
		requireStored(t, client, db, account, "Archive", sentCopyMessage("elsewhere@example.test"), submitted)
		sentRows(t, "elsewhere@example.test", 1)
	})
	t.Run("to a folder named like Sent", func(t *testing.T) {
		// Folder names other than INBOX are case-sensitive (RFC 9051 §5.1).
		submitted := submitToSent(t, db, account, "lowercase@example.test")
		requireStored(t, client, db, account, "sent", sentCopyMessage("lowercase@example.test"), submitted)
		sentRows(t, "lowercase@example.test", 1)
	})
	t.Run("with no Message-ID", func(t *testing.T) {
		// The session makes up an ID for a submission without one, which the app's copy
		// cannot carry. An empty ID on a stored row must not match either.
		generated := submitToSent(t, db, account, "generated@example.test")
		emptyRef, err := gatewaysmtp.NewStore(db, "").PersistSubmittedMessage(gatewaysmtp.SubmittedMessage{Sender: account.Address, Subject: "no id", RawMessage: "Subject: no id\r\n\r\nno id\r\n"})
		if err != nil || emptyRef == nil {
			t.Fatalf("PersistSubmittedMessage: %v, %v", emptyRef, err)
		}
		message := "From: Sender <sender@example.test>\r\nSubject: no id\r\n\r\nthe app's copy\r\n"
		requireStored(t, client, db, account, "Sent", message, max(generated, *emptyRef))
	})
	t.Run("with another Message-ID", func(t *testing.T) {
		submitted := submitToSent(t, db, account, "one@example.test")
		requireStored(t, client, db, account, "Sent", sentCopyMessage("two@example.test"), submitted)
		sentRows(t, "one@example.test", 1)
	})
	t.Run("matching a message that did not come from submission", func(t *testing.T) {
		// What the API's send, a Sent copy made by another route, leaves in Sent.
		row := models.Message{MailboxID: account.ID, Folder: "Sent", MsgID: "<api-send@example.test>", Sender: account.Address, Subject: "sent copy", ReceivedAt: time.Now()}
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
		requireStored(t, client, db, account, "Sent", sentCopyMessage("api-send@example.test"), row.ID)
		sentRows(t, "api-send@example.test", 2)
	})
	t.Run("repeating an upload nothing submitted", func(t *testing.T) {
		message := sentCopyMessage("only-uploaded@example.test")
		first := requireStored(t, client, db, account, "Sent", message, 0)
		requireStored(t, client, db, account, "Sent", message, first)
		sentRows(t, "only-uploaded@example.test", 2)
	})
	t.Run("after the submitted message left Sent", func(t *testing.T) {
		submitted := submitToSent(t, db, account, "moved@example.test")
		if err := db.Model(&models.Message{}).Where("id = ?", submitted).Update("folder", "Archive").Error; err != nil {
			t.Fatal(err)
		}
		requireStored(t, client, db, account, "Sent", sentCopyMessage("moved@example.test"), submitted)
	})
	for _, column := range []string{"imap_deleted", "is_deleted"} {
		t.Run("after the submitted message was marked "+column, func(t *testing.T) {
			// Answering with a message on its way out would lose the only copy.
			messageID := column + "@example.test"
			submitted := submitToSent(t, db, account, messageID)
			if err := db.Model(&models.Message{}).Where("id = ?", submitted).Update(column, true).Error; err != nil {
				t.Fatal(err)
			}
			requireStored(t, client, db, account, "Sent", sentCopyMessage(messageID), submitted)
		})
	}
	t.Run("for another account", func(t *testing.T) {
		other := fx.admin
		otherClient := newIMAPProofClient(t, imapProofAPI(t, db, other))
		submitted := submitToSent(t, db, account, "someone-elses@example.test")
		requireStored(t, otherClient, db, other, "Sent", sentCopyMessage("someone-elses@example.test"), submitted)
		sentRows(t, "someone-elses@example.test", 1)
	})
}

// Only APPEND asks: a COPY into Sent and an import by any other route store as they did.
func TestOnlyAppendMatchesASubmission(t *testing.T) {
	db := importProofDB(t)
	account := seedAccountsFixture(t, db).user
	apiURL := imapProofAPI(t, db, account)
	client := newIMAPProofClient(t, apiURL)
	imported := func(alwaysStore bool, raw string) *apiclient.ImportResponse {
		t.Helper()
		resp, err := apiclient.New(apiURL).ImportMessage("token", account.ID, &apiclient.ImportRequest{
			RawMessage: []byte(raw), Folder: "Sent", InternalDate: time.Now(), AlwaysStore: alwaysStore,
		})
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	t.Run("COPY", func(t *testing.T) {
		const messageID = "copied@example.test"
		submitted := submitToSent(t, db, account, messageID)
		requireStored(t, client, db, account, "INBOX", sentCopyMessage(messageID), submitted)
		client.ok("SELECT INBOX")
		_, tagged := client.ok("COPY 1 Sent")
		if _, destination := copied(t, tagged); destination <= submitted {
			t.Errorf("COPYUID destination = %d, want a new message above %d", destination, submitted)
		}
		client.ok("UNSELECT")
		if stored := folderMessages(t, db, account, "Sent"); len(stored) != 2 {
			t.Errorf("Sent holds %d messages after the COPY, want the submitted one and the copy", len(stored))
		}
	})
	for _, alwaysStore := range []bool{false, true} {
		t.Run(fmt.Sprintf("import with always_store %t", alwaysStore), func(t *testing.T) {
			messageID := fmt.Sprintf("imported-%t@example.test", alwaysStore)
			submitted := submitToSent(t, db, account, messageID)
			resp := imported(alwaysStore, sentCopyMessage(messageID))
			if !resp.Data.Imported || resp.Data.ID == submitted {
				t.Errorf("import = id %d, imported %v; want a new message beside %d", resp.Data.ID, resp.Data.Imported, submitted)
			}
		})
	}
}
