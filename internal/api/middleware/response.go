package middleware

import (
	"encoding/json"
	"net/http"
)

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

func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(ErrorResponse{
		Error: ErrorBody{
			Code:    code,
			Message: message,
		},
	})
}
