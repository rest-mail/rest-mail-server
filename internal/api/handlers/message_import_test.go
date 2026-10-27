package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail/internal/api/endpoint"
	apitypes "github.com/restmail/restmail/internal/api/types"
)

func importMessageRoutes(h *MessageImportHandler) http.Handler {
	r := chi.NewRouter()
	h.Register(endpoint.Routes{Router: r, Registry: endpoint.NewRegistry()}, MailboxRouteChecks{OnMailbox: func(string) endpoint.Middleware { return passThrough }})
	return r
}

func TestMessageImportValidationAndSizeBeforeDatabase(t *testing.T) {
	routes := importMessageRoutes(NewMessageImportHandler(nil, 1024, nil))
	base := apitypes.ImportMessageBody{RawMessage: []byte("Subject: old\r\n\r\nbody\r\n"), InternalDate: time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)}
	for _, tc := range []struct {
		name, path, field string
		status            int
		change            func(*apitypes.ImportMessageBody)
	}{
		{"empty raw", "12", "raw_message", 422, func(b *apitypes.ImportMessageBody) { b.RawMessage = nil }},
		{"missing internal date", "12", "internal_date", 422, func(b *apitypes.ImportMessageBody) { b.InternalDate = time.Time{} }},
		{"bad folder", "12", "folder", 422, func(b *apitypes.ImportMessageBody) { b.Folder = "bad\x00folder" }},
		{"unknown flag", "12", "flags", 422, func(b *apitypes.ImportMessageBody) { b.Flags = []string{"$Custom"} }},
		{"bad id", "0", "id", 422, func(*apitypes.ImportMessageBody) {}},
		{"decoded size", "12", "message_too_large", 413, func(b *apitypes.ImportMessageBody) { b.RawMessage = bytes.Repeat([]byte("x"), 1025) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := base
			tc.change(&body)
			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest(http.MethodPost, "/api/v1/accounts/"+tc.path+"/messages/import", bytes.NewReader(raw))
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			routes.ServeHTTP(w, r)
			if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.field) {
				t.Fatalf("refusal: %d %s", w.Code, w.Body)
			}
		})
	}
	for _, tc := range []struct {
		body   string
		status int
	}{
		{`{"raw_message":"invalid base64!"}`, 422},
		{`{"raw_message":"` + strings.Repeat("A", 68000) + `"}`, 413},
	} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/api/v1/accounts/12/messages/import", strings.NewReader(tc.body))
		r.Header.Set("Content-Type", "application/json")
		routes.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("encoded body refusal: %d %s", w.Code, w.Body)
		}
	}
}
