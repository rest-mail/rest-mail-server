// Package envelope holds the JSON envelopes the REST Mail API wraps its answers
// in. A single resource is {"data": ...}; a collection is {"data": [...],
// "pagination": {...}}.
//
// The server writes these envelopes and describes them in its API document. A
// client decodes them. Both use these types, so a client can read an answer
// without importing any server package.
package envelope

// Data is the {"data": ...} envelope around a single resource.
type Data[T any] struct {
	Data T `json:"data"`
}

// List is the envelope around a collection: {"data": [...], "pagination": {...}}.
type List[T any] struct {
	Data       []T         `json:"data"`
	Pagination *Pagination `json:"pagination,omitempty"`
}

// ListEnvelope marks List, whatever its element type, so the server can tell a
// collection answer from a single resource. It does nothing.
func (List[T]) ListEnvelope() {}

// Pagination is the cursor-based paging information of a collection answer.
type Pagination struct {
	Cursor  string `json:"cursor,omitempty"`
	HasMore bool   `json:"has_more"`
	Total   int64  `json:"total,omitempty"`
}
