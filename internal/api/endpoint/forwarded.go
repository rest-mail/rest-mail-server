package endpoint

import (
	"net/http"
	"reflect"
)

// Forwarded describes a JSON answer as T while writing its exact bytes and status. It
// deliberately bypasses the data envelope and empty-list normalization, for an answer whose
// contract is not the API's: an upstream's (the JavaScript sidecar owns its response
// contract, including user-defined script results), or a protocol document another server
// parses (RESTMAIL discovery). A list T declares is still never null: a route that forwards
// one keeps that promise itself (handlers.scriptLogsListed), since only it can tell the list
// from a look-alike in a script's own result.
type Forwarded[T any] struct {
	Status int
	Body   []byte
}

func (Forwarded[T]) responseType() reflect.Type { return reflect.TypeOf((*T)(nil)).Elem() }
func (f Forwarded[T]) writeResponse(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(f.Status)
	_, _ = w.Write(f.Body)
}

type forwardedResponse interface {
	responseType() reflect.Type
	writeResponse(http.ResponseWriter)
}
