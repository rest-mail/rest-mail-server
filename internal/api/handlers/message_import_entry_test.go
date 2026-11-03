package handlers

import (
	"net/http"

	apitypes "github.com/rest-mail/rest-mail-server/internal/api/types"
)

// importMessage imports a message that has been decoded already, past the route's reading of a
// request, for a test about what is stored.
func (h *MessageImportHandler) importMessage(r *http.Request, in importMessageRequest) (apitypes.ImportMessageResult, error) {
	source, err := newImportSource(h.maxBytes)
	if err != nil {
		return apitypes.ImportMessageResult{}, err
	}
	defer source.remove()
	if err := source.replace(in.Body.RawMessage); err != nil {
		return apitypes.ImportMessageResult{}, err
	}
	return h.storeImportedMessage(r, in.ID, in.Body, source)
}
