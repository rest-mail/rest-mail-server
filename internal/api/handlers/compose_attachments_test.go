package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSendMessageRefusesAttachments(t *testing.T) {
	assertComposeAttachmentsRefused(t, http.MethodPost, "/api/v1/messages/send")
}

func TestSaveDraftRefusesAttachments(t *testing.T) {
	assertComposeAttachmentsRefused(t, http.MethodPost, "/api/v1/messages/draft")
}

func TestUpdateDraftRefusesAttachments(t *testing.T) {
	assertComposeAttachmentsRefused(t, http.MethodPut, "/api/v1/messages/draft/1")
}

func assertComposeAttachmentsRefused(t *testing.T, method, path string) {
	t.Helper()
	routes := composeRoutes(NewMessageHandler(nil, nil, nil, "", nil))
	for _, value := range []string{`[{"filename":"report.txt","content":"aGVsbG8="}]`, `[]`, `null`, `{}`, `"file"`, `false`, `1`} {
		t.Run(value, func(t *testing.T) {
			body := `{"from":"admin@example.test","to":["friend@example.test"],"attachments":` + value + `}`
			request := httptest.NewRequest(method, path, strings.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			routes.ServeHTTP(response, request)
			var answer struct {
				Error struct {
					Code    string `json:"code"`
					Details struct {
						Fields map[string]string `json:"fields"`
					} `json:"details"`
				} `json:"error"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &answer); err != nil {
				t.Fatal(err)
			}
			if response.Code != http.StatusUnprocessableEntity || answer.Error.Code != "validation_failed" || answer.Error.Details.Fields["attachments"] != "attachments cannot be sent through this route yet" {
				t.Fatalf("want 422 validation_failed with explicit attachment refusal, got %d %s", response.Code, response.Body)
			}
		})
	}
	request := httptest.NewRequest(method, path, strings.NewReader(`{"unrecognized":true}`))
	response := httptest.NewRecorder()
	routes.ServeHTTP(response, request)
	if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), `"unrecognized":"not allowed"`) {
		t.Fatalf("unknown field validation changed: %d %s", response.Code, response.Body)
	}
}
