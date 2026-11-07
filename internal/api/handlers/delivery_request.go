package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/rest-mail/rest-mail-server/internal/api/respond"
)

// deliveryImportOnlyFields are the fields a delivery once took that only an import may set
// (#1474). Older callers still send them; a delivery takes no notice of them, so arriving mail
// gets the server's time, the inbound rules' folder and default flags.
var deliveryImportOnlyFields = map[string]bool{
	"folder": true, "is_answered": true, "is_read": true, "is_flagged": true, "is_starred": true,
	"is_draft": true, "received_at": true, "mailbox_id": true,
}

// stripDeliveryImportFields takes those fields out of a whole request body. The delivery route
// itself reads its body from disk and leaves them out as it reads (spooledBodySpec.ignored).
func stripDeliveryImportFields(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body == nil {
			next.ServeHTTP(w, r)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				respond.Error(w, http.StatusRequestEntityTooLarge, "request_too_large", "Request body too large")
			} else {
				respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid request body")
			}
			return
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(body, &fields) == nil {
			changed := false
			for name := range deliveryImportOnlyFields {
				if _, exists := fields[name]; exists {
					delete(fields, name)
					changed = true
				}
			}
			if changed {
				body, err = json.Marshal(fields)
				if err != nil {
					respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid request body")
					return
				}
				r.ContentLength = int64(len(body))
			}
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		next.ServeHTTP(w, r)
	})
}
