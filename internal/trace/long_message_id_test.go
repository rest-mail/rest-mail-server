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

// TestMessageTrace_Varchar255ControlRejectsTheWholeBatch keeps the direct GORM
// batch control from #2219 standing: on a copy of message_traces whose
// rfc_message_id is the old varchar(255), the same mixed short/long batch is
// refused with SQLSTATE 22001 and commits nothing, while the real table commits
// both rows. If the control stops failing, the long-ID tests no longer prove
// anything about the column type.
func TestMessageTrace_Varchar255ControlRejectsTheWholeBatch(t *testing.T) {
	database := openTraceTestDB(t)
	transaction := database.Begin()
	if transaction.Error != nil {
		t.Fatal(transaction.Error)
	}
	t.Cleanup(func() { transaction.Rollback() })
	if err := transaction.Exec(`CREATE TEMP TABLE varchar_trace_control (LIKE message_traces INCLUDING DEFAULTS) ON COMMIT DROP`).Error; err != nil {
		t.Fatalf("create control table: %v", err)
	}
	if err := transaction.Exec(`ALTER TABLE varchar_trace_control ALTER COLUMN rfc_message_id TYPE varchar(255)`).Error; err != nil {
		t.Fatalf("narrow control column: %v", err)
	}
	batch := func() []models.MessageTrace {
		return []models.MessageTrace{
			{RFCMessageID: "<short-control@example.test>", Direction: "inbound", Outcome: "rejected"},
			{RFCMessageID: strings.Repeat("a", 256), Direction: "inbound", Outcome: "rejected"},
		}
	}

	transaction.SavePoint("control")
	err := transaction.Table("varchar_trace_control").CreateInBatches(batch(), 10).Error
	if err == nil || !strings.Contains(err.Error(), "22001") {
		t.Fatalf("varchar(255) control batch error = %v, want SQLSTATE 22001", err)
	}
	transaction.RollbackTo("control")
	var controlRows int64
	if err := transaction.Table("varchar_trace_control").Count(&controlRows).Error; err != nil {
		t.Fatalf("count control rows: %v", err)
	}
	if controlRows != 0 {
		t.Errorf("varchar(255) control committed %d rows, want 0 (the refusal is atomic)", controlRows)
	}

	rows := batch()
	if err := transaction.CreateInBatches(rows, 10).Error; err != nil {
		t.Fatalf("message_traces batch: %v", err)
	}
	var stored int64
	if err := transaction.Model(&models.MessageTrace{}).Where("id IN ?", []uint{rows[0].ID, rows[1].ID}).Count(&stored).Error; err != nil {
		t.Fatalf("count committed rows: %v", err)
	}
	if stored != 2 {
		t.Errorf("message_traces committed %d rows, want 2", stored)
	}
}
