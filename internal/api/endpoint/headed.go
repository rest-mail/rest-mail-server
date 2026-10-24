package endpoint

import (
	"net/http"
	"reflect"
)

// Headed is an answer with response headers its handler sets, such as the cookies a sign-in
// writes. Header is written first, and Value is answered as it would be on its own: in the
// data envelope, as a List, as no body, or with a Status. Operation.Headers describes the
// headers on every success response.
type Headed[T any] struct {
	Header http.Header
	Value  T
}

func (h Headed[T]) responseHeader() (http.Header, interface{}) { return h.Header, h.Value }
func (Headed[T]) headedType() reflect.Type                     { return reflect.TypeOf((*T)(nil)).Elem() }
func (Headed[T]) headedEnvelope() reflect.Type                 { return reflect.TypeOf((*Data[T])(nil)).Elem() }

type headedResponse interface {
	responseHeader() (http.Header, interface{})
	headedType() reflect.Type
	headedEnvelope() reflect.Type
}

// unheaded writes out's headers, if it carries any, and returns the answer they came with.
func unheaded(w http.ResponseWriter, out interface{}) interface{} {
	headed, ok := out.(headedResponse)
	if !ok {
		return out
	}
	header, value := headed.responseHeader()
	for name, values := range header {
		for _, v := range values {
			w.Header().Add(name, v)
		}
	}
	return value
}
