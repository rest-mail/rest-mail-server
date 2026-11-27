package handlers

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
	"github.com/rest-mail/rest-mail-server/internal/trace"
)

// TestDeliverMessage_LongMessageIDTracesSurviveTheRecorder is TRACE-04 of #2117:
// it sends a short and a long Message-ID through the real delivery route, an
// inbound pipeline and the production trace recorder, then checks that both
// messages are stored and that the recorder committed a delivered trace for each,
// linked to its message and carrying the Message-ID intact. Before the trace
// column became text, the 256-character ID made PostgreSQL reject the whole
// recorder batch, so the short-ID trace was lost with it although both
// deliveries answered 201.
func TestDeliverMessage_LongMessageIDTracesSurviveTheRecorder(t *testing.T) {
	db := openMsgDeleteTestDB(t)
	if err := migrateModels(db, &models.QuotaUsage{}, &models.Pipeline{}, &models.SieveScript{}, &models.MessageTrace{}); err != nil {
		t.Skipf("deliver DB test skipped: migrate failed (%v)", err)
	}
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })
	fx := seedMsgDeleteFixture(t, tx, 0)
	if err := tx.Create(&models.Pipeline{DomainID: fx.mailbox.DomainID, Direction: "inbound", Active: true, Filters: json.RawMessage("[]")}).Error; err != nil {
		t.Fatal(err)
	}

	recorder := trace.NewRecorder(tx, trace.Config{SampleRate: 1})
	recorder.Start()
	t.Cleanup(recorder.Shutdown)
	h := NewMessageHandler(tx, nil, pipeline.NewEngine(withQuietVacation(pipeline.NewRegistry()), nil), "", recorder)

	messageIDs := []string{
		"<" + strings.Repeat("a", 256-len("<@example.test>")) + "@example.test>",
		"<short@example.test>",
	}
	for index, messageID := range messageIDs {
		subject := fmt.Sprintf("Trace %d", index)
		raw := "From: someone@remote.test\r\nTo: " + fx.mailbox.Address + "\r\nSubject: " + subject +
			"\r\nDate: Mon, 02 Nov 2026 10:00:00 +0000\r\nMessage-ID: " + messageID + "\r\n\r\nhello\r\n"
		body, err := json.Marshal(map[string]string{
			"address":     fx.mailbox.Address,
			"sender":      "someone@remote.test",
			"subject":     subject,
			"message_id":  messageID,
			"raw_message": base64.StdEncoding.EncodeToString([]byte(raw)),
		})
		if err != nil {
			t.Fatal(err)
		}
		rr := httptest.NewRecorder()
		deliveryRoute(h)(rr, httptest.NewRequest(http.MethodPost, "/api/v1/messages/deliver", strings.NewReader(string(body))))
		if rr.Code != http.StatusCreated {
			t.Fatalf("deliver %d-character Message-ID: got %d (%s), want 201", len(messageID), rr.Code, rr.Body.String())
		}
	}
	// Shutdown flushes everything recorded, so the batch holds both traces.
	recorder.Shutdown()
	// The recorder writes through the test transaction, so a refused batch insert
	// aborts it; say so instead of failing on the next unrelated read.
	if err := tx.Exec("SELECT 1").Error; err != nil {
		t.Fatalf("the recorder's batch insert failed and dropped its traces: %v", err)
	}

	for _, messageID := range messageIDs {
		var message models.Message
		if err := tx.Where("mailbox_id = ? AND message_id = ?", fx.mailbox.ID, messageID).First(&message).Error; err != nil {
			t.Fatalf("stored message for %d-character Message-ID: %v", len(messageID), err)
		}
		var traces []models.MessageTrace
		if err := tx.Where("rfc_message_id = ?", messageID).Find(&traces).Error; err != nil {
			t.Fatalf("read traces for %d-character Message-ID: %v", len(messageID), err)
		}
		if len(traces) != 1 {
			t.Fatalf("%d-character Message-ID: %d traces committed, want 1", len(messageID), len(traces))
		}
		got := traces[0]
		if got.Outcome != outcomeDelivered {
			t.Errorf("%d-character Message-ID: trace outcome = %q, want %q", len(messageID), got.Outcome, outcomeDelivered)
		}
		if got.MessageID == nil || *got.MessageID != message.ID {
			t.Errorf("%d-character Message-ID: trace message_id = %v, want stored message %d", len(messageID), got.MessageID, message.ID)
		}
		if got.RFCMessageID != messageID {
			t.Errorf("trace Message-ID = %q, want intact %q", got.RFCMessageID, messageID)
		}
	}
}
