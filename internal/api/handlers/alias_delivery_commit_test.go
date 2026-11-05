package handlers

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/db/repositories"
	"gorm.io/gorm"
)

func TestAliasDeliveryCommitAndReplay(t *testing.T) {
	for _, destination := range []string{"hosted", "external"} {
		t.Run(destination, func(t *testing.T) {
			fixture := newAliasDeliveryFixture(t)
			if err := migrateModels(fixture.db, &models.InboundMessage{}); err != nil {
				t.Fatal(err)
			}
			if destination == "external" {
				if err := fixture.db.Model(&fixture.alias).Update("destination_address", "destination@elsewhere.test").Error; err != nil {
					t.Fatal(err)
				}
			}
			recipient, err := repositories.ResolveRecipient(fixture.db, fixture.alias.SourceAddress, false)
			if err != nil {
				t.Fatal(err)
			}
			params := localDeliveryParams{Mailbox: recipient.Mailbox, Sender: "sender@outside.test", Subject: "staged alias", BodyText: "body",
				RawMessage: "From: sender@outside.test\r\nSubject: staged alias\r\n\r\nbody\r\n", Stage: deliverRequired, State: newDeliveryState()}
			if _, err := fixture.messages.deliverToRecipient(t.Context(), recipient, params); !errors.Is(err, errContentStageNext) {
				t.Fatalf("required stage = %v", err)
			}
			var local, outgoing int64
			fixture.db.Model(&models.Message{}).Where("mailbox_id = ?", fixture.mailbox.ID).Count(&local)
			fixture.db.Model(&models.OutboundQueue{}).Where("sending_domain = ?", fixture.domain.Name).Count(&outgoing)
			if local != 0 || outgoing != 0 {
				t.Fatalf("required stage wrote local=%d outgoing=%d", local, outgoing)
			}
			stored, err := json.Marshal(params.State)
			if err != nil {
				t.Fatal(err)
			}
			params.State = newDeliveryState()
			if err := json.Unmarshal(stored, params.State); err != nil {
				t.Fatal(err)
			}
			params.Stage = deliverContent
			pending := models.InboundMessage{ObjectRef: fixture.domain.Name, MailFrom: params.Sender, Recipient: fixture.alias.SourceAddress, Size: int64(len(params.RawMessage)), Checksum: "test"}
			if err := repositories.NewInboundQueue(fixture.db).Enqueue(&pending, time.Now()); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { fixture.db.Delete(&models.InboundMessage{}, pending.ID) })
			injected := errors.New("injected commit failure")
			params.InCommit = func(tx *gorm.DB) error {
				if err := repositories.NewInboundQueue(tx).Finish(pending.ID); err != nil {
					return err
				}
				return injected
			}
			if _, err := fixture.messages.deliverToRecipient(t.Context(), recipient, params); !errors.Is(err, injected) {
				t.Fatalf("failed commit = %v", err)
			}
			var pendingCount int64
			fixture.db.Model(&models.InboundMessage{}).Where("id = ?", pending.ID).Count(&pendingCount)
			if pendingCount != 1 {
				t.Fatal("failed transaction consumed the inbound row")
			}
			params.InCommit = func(tx *gorm.DB) error { return repositories.NewInboundQueue(tx).Finish(pending.ID) }
			if _, err := fixture.messages.deliverToRecipient(t.Context(), recipient, params); err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.messages.deliverToRecipient(t.Context(), recipient, params); !errors.Is(err, repositories.ErrInboundMessageNotFound) {
				t.Fatalf("stale replay = %v", err)
			}
			fixture.db.Model(&models.Message{}).Where("mailbox_id = ?", fixture.mailbox.ID).Count(&local)
			fixture.db.Model(&models.OutboundQueue{}).Where("sending_domain = ?", fixture.domain.Name).Count(&outgoing)
			if destination == "hosted" && (local != 1 || outgoing != 0) {
				t.Fatalf("hosted replay duplicated: local=%d outgoing=%d", local, outgoing)
			}
			if destination == "external" && (local != 0 || outgoing != 1) {
				t.Fatalf("external replay duplicated: local=%d outgoing=%d", local, outgoing)
			}
		})
	}
}
