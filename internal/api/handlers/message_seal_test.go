package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/api/middleware"
	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
)

func TestMessageSealDeliveryVerificationAndAuthorization(t *testing.T) {
	database, err := connectTestDB()
	if err != nil {
		t.Fatal(err)
	}
	if err := database.AutoMigrate(append(accountModels, &models.Message{}, &models.Attachment{})...); err != nil {
		t.Fatal(err)
	}
	tx := database.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	fixture := seedMsgDeleteFixture(t, tx, 0)
	handler := NewMessageHandler(tx, nil, nil, "", nil)
	message, err := handler.deliverToLocal(context.Background(), localDeliveryParams{
		Mailbox: fixture.mailbox, Sender: "sender@example.test", Subject: "Receipt", BodyText: "original body",
		RawMessage: "From: sender@example.test\r\nSubject: Receipt\r\n\r\noriginal body\r\n",
	})
	if err != nil || message == nil {
		t.Fatalf("delivery: %v, %v", message, err)
	}
	if len(message.SealedHash) != 64 {
		t.Fatalf("delivery did not seal: %+v", message)
	}
	seal := message.SealedHash
	caller := &auth.Claims{AccountID: fixture.mailbox.ID}
	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), middleware.ClaimsKey, caller)))
		})
	})
	handler.RegisterMessages(endpoint.Routes{Router: router, Registry: endpoint.NewRegistry()})
	request := func(method, suffix, body string) *httptest.ResponseRecorder {
		t.Helper()
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(method, fmt.Sprintf("/api/v1/messages/%d%s", message.ID, suffix), strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(recorder, req)
		return recorder
	}
	assertState := func(want string) {
		t.Helper()
		response := request(http.MethodGet, "/seal", "")
		var result struct {
			Data struct {
				State      string `json:"state"`
				SealedHash string `json:"sealed_hash"`
			} `json:"data"`
		}
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &result) != nil || result.Data.State != want {
			t.Fatalf("want %s: HTTP %d %s", want, response.Code, response.Body.String())
		}
		if want != "not_sealed" && result.Data.SealedHash != seal {
			t.Fatalf("verification changed the acceptance seal: %s", response.Body.String())
		}
		if want == "not_sealed" && result.Data.SealedHash != "" {
			t.Fatalf("unsealed message answered a seal: %s", response.Body.String())
		}
	}
	assertState("intact")
	if response := request(http.MethodGet, "", ""); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"sealed_hash":"`+seal+`"`) {
		t.Fatalf("message omits digest: %d %s", response.Code, response.Body.String())
	}
	if response := request(http.MethodPatch, "", `{"is_read":true,"is_flagged":true,"folder":"Archive"}`); response.Code != http.StatusOK {
		t.Fatalf("flags/move: %d %s", response.Code, response.Body.String())
	}
	assertState("intact")
	for _, update := range []map[string]any{
		{"raw_message": "From: sender@example.test\r\nSubject: Receipt\r\n\r\nedited body\r\n"},
		{"raw_message": "From: sender@example.test\r\nSubject: Edited\r\n\r\noriginal body\r\n"},
		{"body_text": "edited displayed body"},
		{"headers": models.JSONB(`{"Subject":["Edited"]}`)},
	} {
		if err := tx.Model(&models.Message{}).Where("id = ?", message.ID).Updates(update).Error; err != nil {
			t.Fatal(err)
		}
		assertState("mismatch")
		if err := tx.Model(&models.Message{}).Where("id = ?", message.ID).Updates(map[string]any{"raw_message": message.RawMessage, "body_text": message.BodyText, "headers": message.Headers}).Error; err != nil {
			t.Fatal(err)
		}
		assertState("intact")
	}
	if err := tx.Model(&models.Message{}).Where("id = ?", message.ID).UpdateColumn("sealed_hash", nil).Error; err != nil {
		t.Fatal(err)
	}
	assertState("not_sealed")
	if response := request(http.MethodPatch, "", `{"is_read":false}`); response.Code != http.StatusOK {
		t.Fatalf("update historical message: %d %s", response.Code, response.Body.String())
	}
	assertState("not_sealed")
	caller = &auth.Claims{AccountID: fixture.mailbox.ID + 100000}
	if response := request(http.MethodGet, "/seal", ""); response.Code != http.StatusNotFound {
		t.Fatalf("another caller got seal: %d %s", response.Code, response.Body.String())
	}
	caller = nil
	if response := request(http.MethodGet, "/seal", ""); response.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous caller got seal: %d %s", response.Code, response.Body.String())
	}
}
