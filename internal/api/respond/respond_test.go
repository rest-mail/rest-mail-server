package respond

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestJSON(t *testing.T) {
	w := httptest.NewRecorder()

	payload := map[string]string{"greeting": "hello"}
	JSON(w, http.StatusCreated, payload)

	resp := w.Result()

	if resp.StatusCode != http.StatusCreated {
		t.Errorf("expected status %d, got %d", http.StatusCreated, resp.StatusCode)
	}

	ct := resp.Header.Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("expected Content-Type %q, got %q", "application/json", ct)
	}

	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}

	if body["greeting"] != "hello" {
		t.Errorf("expected greeting %q, got %q", "hello", body["greeting"])
	}
}

func TestData(t *testing.T) {
	w := httptest.NewRecorder()

	resource := map[string]string{"id": "42", "name": "test"}
	Data(w, http.StatusOK, resource)

	resp := w.Result()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, resp.StatusCode)
	}

	var body DataResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}

	inner, ok := body.Data.(map[string]interface{})
	if !ok {
		t.Fatalf("expected data to be a map, got %T", body.Data)
	}

	if inner["id"] != "42" {
		t.Errorf("expected id %q, got %v", "42", inner["id"])
	}
	if inner["name"] != "test" {
		t.Errorf("expected name %q, got %v", "test", inner["name"])
	}
}

func TestList(t *testing.T) {
	w := httptest.NewRecorder()

	items := []map[string]string{
		{"id": "1"},
		{"id": "2"},
	}
	pag := &Pagination{
		Cursor:  "abc123",
		HasMore: true,
		Total:   10,
	}
	List(w, items, pag)

	resp := w.Result()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, resp.StatusCode)
	}

	var body ListResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}

	dataSlice, ok := body.Data.([]interface{})
	if !ok {
		t.Fatalf("expected data to be a slice, got %T", body.Data)
	}
	if len(dataSlice) != 2 {
		t.Errorf("expected 2 items, got %d", len(dataSlice))
	}

	if body.Pagination == nil {
		t.Fatal("expected pagination to be present")
	}
	if body.Pagination.Cursor != "abc123" {
		t.Errorf("expected cursor %q, got %q", "abc123", body.Pagination.Cursor)
	}
	if !body.Pagination.HasMore {
		t.Error("expected has_more to be true")
	}
	if body.Pagination.Total != 10 {
		t.Errorf("expected total 10, got %d", body.Pagination.Total)
	}
}

func TestList_NoPagination(t *testing.T) {
	w := httptest.NewRecorder()

	items := []string{"a", "b"}
	List(w, items, nil)

	resp := w.Result()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, resp.StatusCode)
	}

	// Decode into raw JSON to verify "pagination" key is absent.
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}

	if _, exists := raw["pagination"]; exists {
		t.Error("expected pagination key to be omitted when nil")
	}

	if _, exists := raw["data"]; !exists {
		t.Error("expected data key to be present")
	}
}

func TestError(t *testing.T) {
	w := httptest.NewRecorder()

	Error(w, http.StatusNotFound, "not_found", "Resource not found")

	resp := w.Result()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected status %d, got %d", http.StatusNotFound, resp.StatusCode)
	}

	var body ErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}

	if body.Error.Code != "not_found" {
		t.Errorf("expected code %q, got %q", "not_found", body.Error.Code)
	}
	if body.Error.Message != "Resource not found" {
		t.Errorf("expected message %q, got %q", "Resource not found", body.Error.Message)
	}
}

func TestValidationError(t *testing.T) {
	w := httptest.NewRecorder()

	fields := map[string]string{
		"email":    "is required",
		"password": "must be at least 8 characters",
	}
	ValidationError(w, fields)

	resp := w.Result()

	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("expected status %d, got %d", http.StatusUnprocessableEntity, resp.StatusCode)
	}

	var body ErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}

	if body.Error.Code != "validation_failed" {
		t.Errorf("expected code %q, got %q", "validation_failed", body.Error.Code)
	}
	if body.Error.Message != "Request body failed validation" {
		t.Errorf("expected message %q, got %q", "Request body failed validation", body.Error.Message)
	}

	// Details is deserialized as map[string]interface{} by encoding/json.
	details, ok := body.Error.Details.(map[string]interface{})
	if !ok {
		t.Fatalf("expected details to be a map, got %T", body.Error.Details)
	}

	fieldsRaw, ok := details["fields"]
	if !ok {
		t.Fatal("expected 'fields' key in details")
	}

	fieldsMap, ok := fieldsRaw.(map[string]interface{})
	if !ok {
		t.Fatalf("expected fields to be a map, got %T", fieldsRaw)
	}

	if fieldsMap["email"] != "is required" {
		t.Errorf("expected email error %q, got %v", "is required", fieldsMap["email"])
	}
	if fieldsMap["password"] != "must be at least 8 characters" {
		t.Errorf("expected password error %q, got %v", "must be at least 8 characters", fieldsMap["password"])
	}
}
