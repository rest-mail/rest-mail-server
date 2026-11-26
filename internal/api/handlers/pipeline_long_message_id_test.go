package handlers

import (
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/pipeline/filters"
)

func TestListPipelineLogs_ExactLongMessageIDLookup(t *testing.T) {
	database := openTraceTestDB(t)
	for _, length := range []int{256, 995} {
		t.Run(strconv.Itoa(length), func(t *testing.T) {
			transaction := database.Begin()
			if transaction.Error != nil {
				t.Fatal(transaction.Error)
			}
			t.Cleanup(func() { transaction.Rollback() })
			messageID := "<" + strings.Repeat("a", length-len("<@example.test>")) + "@example.test>"
			nearMatch := messageID[:len(messageID)-2] + "u>"
			rows := []models.MessageTrace{
				{RFCMessageID: nearMatch, Outcome: "rejected", Stages: json.RawMessage(`[]`)},
				{RFCMessageID: messageID, Outcome: "rejected", Stages: json.RawMessage(`[]`)},
				{RFCMessageID: "<short@example.test>", Outcome: "rejected", Stages: json.RawMessage(`[]`)},
			}
			if err := transaction.Create(&rows).Error; err != nil {
				t.Fatalf("seed traces for exact %d-character lookup: %v", length, err)
			}
			handler := NewPipelineHandler(transaction, nil, filters.JSSidecar{})
			query := url.Values{"rfc_message_id": {messageID}}.Encode()
			got := listTraces(t, handler, query)
			if len(got) != 1 || got[0].ID != rows[1].ID || got[0].RFCMessageID != messageID {
				t.Fatalf("exact long Message-ID lookup = %+v, want only trace %d with intact Message-ID", got, rows[1].ID)
			}
			missingID := messageID[:len(messageID)-2] + "v>"
			if got := listTraces(t, handler, url.Values{"rfc_message_id": {missingID}}.Encode()); len(got) != 0 {
				t.Fatalf("absent long Message-ID lookup = %+v, want no traces", got)
			}
		})
	}
}
