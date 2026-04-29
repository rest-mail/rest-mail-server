package respond

import (
	"encoding/json"
	"net/http"
)

// DataResponse wraps a single resource.
type DataResponse struct {
	Data interface{} `json:"data"`
}

// ListResponse wraps a collection with pagination.
type ListResponse struct {
	Data       interface{} `json:"data"`
	Pagination *Pagination `json:"pagination,omitempty"`
}

// Pagination contains cursor-based pagination info.
type Pagination struct {
	Cursor  string `json:"cursor,omitempty"`
	HasMore bool   `json:"has_more"`
	Total   int64  `json:"total,omitempty"`
}

// ErrorResponse is the standard error response format.
type ErrorResponse struct {
	Error ErrorBody `json:"error"`
}

// ErrorBody contains the error details.
type ErrorBody struct {
	Code    string      `json:"code"`
	Message string      `json:"message"`
	Details interface{} `json:"details,omitempty"`
}

// JSON writes a JSON response with the given status code.
func JSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

// Data writes a single resource wrapped in {"data": ...}.
func Data(w http.ResponseWriter, status int, data interface{}) {
	JSON(w, status, DataResponse{Data: data})
}

// List writes a collection wrapped in {"data": [...], "pagination": {...}}.
func List(w http.ResponseWriter, data interface{}, pagination *Pagination) {
	JSON(w, http.StatusOK, ListResponse{Data: data, Pagination: pagination})
}

// Error writes a standard error response.
func Error(w http.ResponseWriter, status int, code, message string) {
	JSON(w, status, ErrorResponse{
		Error: ErrorBody{
			Code:    code,
			Message: message,
		},
	})
}

// ValidationError writes a 422 validation error with field details.
func ValidationError(w http.ResponseWriter, fields map[string]string) {
	JSON(w, http.StatusUnprocessableEntity, ErrorResponse{
		Error: ErrorBody{
			Code:    "validation_failed",
			Message: "Request body failed validation",
			Details: map[string]interface{}{
				"fields": fields,
			},
		},
	})
}
