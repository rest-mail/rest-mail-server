package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apitypes "github.com/rest-mail/rest-mail-server/internal/api/types"
)

// A set the handler could never transfer is refused before the database.
func TestMessageTransferValidationBeforeDatabase(t *testing.T) {
	routes := transferRoutes(NewMessageTransferHandler(nil, nil))
	many := make([]uint, 1001)
	for i := range many {
		many[i] = uint(i + 1)
	}
	for _, tc := range []struct {
		name, field string
		body        apitypes.TransferMessagesBody
	}{
		{"no ids", "message_ids", apitypes.TransferMessagesBody{SourceFolder: "INBOX", Folder: "Archive"}},
		{"too many", "message_ids", apitypes.TransferMessagesBody{SourceFolder: "INBOX", Folder: "Archive", MessageIDs: many}},
		{"repeated", "message_ids", apitypes.TransferMessagesBody{SourceFolder: "INBOX", Folder: "Archive", MessageIDs: []uint{1, 1}}},
		{"zero", "message_ids", apitypes.TransferMessagesBody{SourceFolder: "INBOX", Folder: "Archive", MessageIDs: []uint{0}}},
		{"no source", "source_folder", apitypes.TransferMessagesBody{Folder: "Archive", MessageIDs: []uint{1}}},
		{"no folder", "folder", apitypes.TransferMessagesBody{SourceFolder: "INBOX", MessageIDs: []uint{1}}},
		{"bad folder", "folder", apitypes.TransferMessagesBody{SourceFolder: "INBOX", Folder: "bad\x00", MessageIDs: []uint{1}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			encoded, _ := json.Marshal(tc.body)
			request := authzRequest(http.MethodPost, "/api/v1/accounts/1/messages/transfer", 1, nil, string(encoded))
			request.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			routes.ServeHTTP(w, request)
			if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), `"`+tc.field+`"`) {
				t.Fatalf("%d %s, want 422 naming %s", w.Code, w.Body, tc.field)
			}
		})
	}
}
