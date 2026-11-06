package apiclient

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	apitypes "github.com/rest-mail/rest-mail-server/internal/api/types"
)

func TestTypedMessageUpdatePreservesFalseFlags(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body apitypes.UpdateMessageBody
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if r.Method != http.MethodPatch || body.IsRead == nil || *body.IsRead || body.IsFlagged == nil || *body.IsFlagged || body.IsStarred != nil || body.Folder != nil {
			t.Error("false flags or omitted fields changed on the wire")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	read, flagged := false, false
	if err := New(srv.URL).UpdateMessage("test-token", 7, MessageUpdate{IsRead: &read, IsFlagged: &flagged}); err != nil {
		t.Fatal(err)
	}
}

func TestSharedClientReadsCanonicalMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(endpoint.Data[apitypes.Message]{Data: apitypes.Message{
			ID: 7, MsgID: "message@example.com", BodyText: "stored body", RawSize: 123, SizeBytes: 50,
		}})
	}))
	defer srv.Close()
	response, err := New(srv.URL).GetMessage("test-token", 7)
	if err != nil {
		t.Fatal(err)
	}
	if response.Data.ID != 7 || response.Data.MsgID != "message@example.com" || response.Data.BodyText != "stored body" || response.Data.WireSize() != 123 {
		t.Fatal("the canonical message did not survive the client envelope")
	}
}

func TestDeliveryProcessedUsesRegisteredAlternative(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(endpoint.Data[apitypes.DeliveryProcessed]{Data: apitypes.DeliveryProcessed{Status: "processed"}})
	}))
	defer srv.Close()
	response, err := New(srv.URL).DeliverMessage(&DeliverRequest{Address: "user@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if response.Processed == nil || response.Processed.Status != "processed" || response.Data.ID != 0 {
		t.Fatal("the registered processed alternative was lost or invented a mailbox message")
	}
}
