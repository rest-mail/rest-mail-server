package trace

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
)

func TestMessageTrace_AcceptedLongMessageIDsStoredIntact(t *testing.T) {
	database := openTraceTestDB(t)
	for _, length := range []int{256, 995} {
		t.Run(strconv.Itoa(length), func(t *testing.T) {
			transaction := database.Begin()
			if transaction.Error != nil {
				t.Fatal(transaction.Error)
			}
			t.Cleanup(func() { transaction.Rollback() })
			messageID := "<" + strings.Repeat("a", length-len("<@example.test>")) + "@example.test>"
			row := models.MessageTrace{RFCMessageID: messageID, Direction: "inbound", Outcome: "rejected"}
			if err := transaction.Create(&row).Error; err != nil {
				t.Fatalf("store %d-character Message-ID: %v", length, err)
			}
			var stored models.MessageTrace
			if err := transaction.First(&stored, row.ID).Error; err != nil {
				t.Fatalf("reload trace: %v", err)
			}
			if stored.RFCMessageID != messageID {
				t.Fatalf("stored Message-ID = %q, want intact %q", stored.RFCMessageID, messageID)
			}
		})
	}
	var indexMethod string
	if err := database.Raw(`SELECT am.amname FROM pg_class AS idx
		JOIN pg_am AS am ON am.oid = idx.relam
		WHERE idx.oid = 'idx_message_traces_rfc_message_id'::regclass`).Scan(&indexMethod).Error; err != nil {
		t.Fatalf("inspect Message-ID lookup index: %v", err)
	}
	if indexMethod != "hash" {
		t.Errorf("Message-ID lookup index method = %q, want hash (fixed-size keys)", indexMethod)
	}
}

func TestRecorder_MixedShortAndLongMessageIDBatchCommitsEveryRow(t *testing.T) {
	for _, batchSize := range []int{4, 5} {
		t.Run("batch_size_"+strconv.Itoa(batchSize), func(t *testing.T) {
			database := openTraceTestDB(t)
			messageIDs := []string{
				"<short-before@example.test>",
				"<" + strings.Repeat("a", 256-len("<@example.test>")) + "@example.test>",
				"<" + strings.Repeat("b", 995-len("<@example.test>")) + "@example.test>",
				"<short-after@example.test>",
			}
			t.Cleanup(func() {
				if err := database.Where("rfc_message_id IN ?", messageIDs).Delete(&models.MessageTrace{}).Error; err != nil {
					t.Errorf("clean up recorded traces: %v", err)
				}
			})
			recorder := newRecorder(database, len(messageIDs), batchSize, time.Hour)
			recorder.Start()
			t.Cleanup(recorder.Shutdown)
			for _, messageID := range messageIDs {
				recorder.Record(models.MessageTrace{RFCMessageID: messageID, Direction: "inbound", Outcome: "rejected"})
			}
			recorder.Shutdown()
			var stored []models.MessageTrace
			if err := database.Where("rfc_message_id IN ?", messageIDs).Order("id").Find(&stored).Error; err != nil {
				t.Fatalf("read committed recorder batch: %v", err)
			}
			if len(stored) != len(messageIDs) {
				t.Fatalf("committed %d traces, want %d (both short and long IDs)", len(stored), len(messageIDs))
			}
			for index, row := range stored {
				if row.RFCMessageID != messageIDs[index] {
					t.Errorf("trace %d Message-ID = %q, want %q", index, row.RFCMessageID, messageIDs[index])
				}
			}
		})
	}
}
