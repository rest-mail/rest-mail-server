package endpoint

import (
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/go-chi/chi/v5"
)

// unflushable is a response writer that cannot flush.
type unflushable struct{ http.ResponseWriter }

// A Stream goes out with its headers and the operation's status at once, then as Send writes
// and flushes it; Done is called once it is over, sent or not. A writer that cannot flush is
// a 500, and Send never runs. It is described as text in its media type.
func TestStreamIsSentAsItIsWritten(t *testing.T) {
	var sent, done int
	r := chi.NewRouter()
	reg := NewRegistry()
	Register(Routes{Router: r, Registry: reg}, Operation{Method: http.MethodGet, Path: "/events", OperationID: "events",
		MediaType: "text/event-stream"}, func(*http.Request, struct{}) (Stream, error) {
		return Stream{ContentType: "text/event-stream", Header: http.Header{"Cache-Control": {"no-cache"}},
			Send: func(w io.Writer, flush func()) {
				sent++
				_, _ = io.WriteString(w, "event: a\ndata: {}\n\n")
				flush()
				_, _ = io.WriteString(w, ": keepalive\n\n")
				flush()
			},
			Done: func() { done++ }}, nil
	})

	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/events", nil))
	if rr.Code != http.StatusOK || rr.Body.String() != "event: a\ndata: {}\n\n: keepalive\n\n" || !rr.Flushed ||
		rr.Header().Get("Content-Type") != "text/event-stream" || rr.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("stream = %d %q flushed=%v %v", rr.Code, rr.Body, rr.Flushed, rr.Header())
	}
	if sent != 1 || done != 1 {
		t.Fatalf("sent %d, done %d; want 1 and 1", sent, done)
	}

	rr = httptest.NewRecorder()
	r.ServeHTTP(unflushable{rr}, httptest.NewRequest(http.MethodGet, "/events", nil))
	if rr.Code != http.StatusInternalServerError || rr.Header().Get("Content-Type") == "text/event-stream" {
		t.Fatalf("unflushable = %d %v %q", rr.Code, rr.Header(), rr.Body)
	}
	if sent != 1 || done != 2 {
		t.Fatalf("unflushable: sent %d, done %d; want Send not run and Done called", sent, done)
	}

	ok := reg.Endpoints()[0].Responses[0]
	if ok.Status != http.StatusOK || ok.MediaType != "text/event-stream" || ok.Type != reflect.TypeOf("") {
		t.Fatalf("described as %+v", ok)
	}
}

// A handler that refuses answers its error, not a stream.
func TestStreamRefusedBeforeItOpens(t *testing.T) {
	r := chi.NewRouter()
	Register(Routes{Router: r, Registry: NewRegistry()}, Operation{Method: http.MethodGet, Path: "/events", OperationID: "events",
		MediaType: "text/event-stream", Errors: []int{http.StatusServiceUnavailable}}, func(*http.Request, struct{}) (Stream, error) {
		return Stream{}, Refuse(http.StatusServiceUnavailable, "too_many_streams", "Too many concurrent event streams")
	})
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/events", nil))
	if rr.Code != http.StatusServiceUnavailable || rr.Body.String() != `{"error":{"code":"too_many_streams","message":"Too many concurrent event streams"}}`+"\n" {
		t.Fatalf("refused = %d %q", rr.Code, rr.Body)
	}
}

// Like a Document, a Stream needs the operation to name its media type.
func TestStreamNeedsAMediaType(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("a Stream without a MediaType registered")
		}
	}()
	Register(Routes{Router: chi.NewRouter(), Registry: NewRegistry()}, Operation{Method: http.MethodGet, Path: "/events", OperationID: "events"},
		func(*http.Request, struct{}) (Stream, error) { return Stream{}, nil })
}
