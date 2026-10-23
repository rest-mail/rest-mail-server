package endpoint

import (
	"net/http"
	"reflect"
)

// Forwarded describes an upstream JSON answer as T while forwarding its exact bytes and
// status. It deliberately bypasses the data envelope and empty-list normalization: the
// JavaScript sidecar owns its response contract, including user-defined script results.
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
