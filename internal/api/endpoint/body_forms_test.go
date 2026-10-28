package endpoint

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/go-chi/chi/v5"
)

// uploadWidget takes a widget as JSON, or as the raw bytes of a widget file.
type uploadWidget struct {
	JSON *createWidget `body:"application/json"`
	File *[]byte       `body:"application/vnd.widget"`
}

// A body taken in several media types is bound by its Content-Type, is the 400 in
// another, and the 400 is described; the handler can read the body again as it came.
func TestABodyInSeveralForms(t *testing.T) {
	r := chi.NewRouter()
	reg := NewRegistry()
	Register(Routes{Router: r, Registry: reg}, Operation{Method: http.MethodPost, Path: "/widgets", OperationID: "uploadWidget",
		Status: http.StatusCreated}, func(req *http.Request, in uploadWidget) (widget, error) {
		raw, err := io.ReadAll(req.Body)
		if err != nil {
			return widget{}, err
		}
		switch {
		case in.JSON != nil:
			return widget{Name: in.JSON.Name + "|" + string(raw)}, nil
		case in.File != nil:
			return widget{Name: "file:" + string(*in.File)}, nil
		}
		return widget{}, Refuse(http.StatusBadRequest, "bad_request", "no body")
	})

	send := func(contentType, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/widgets", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", contentType)
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		return rr
	}
	if rr := send("application/json", `{"name":"a"}`); rr.Code != http.StatusCreated || !strings.Contains(rr.Body.String(), `"name":"a|{\"name\":\"a\"}"`) {
		t.Errorf("JSON: %d %s", rr.Code, rr.Body)
	}
	if rr := send("application/vnd.widget", "bytes"); rr.Code != http.StatusCreated || !strings.Contains(rr.Body.String(), `"file:bytes"`) {
		t.Errorf("file: %d %s", rr.Code, rr.Body)
	}
	rr := send("text/plain", "x")
	var refused struct {
		Error struct{ Code, Message string } `json:"error"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &refused)
	if rr.Code != http.StatusBadRequest || refused.Error.Code != "bad_request" ||
		refused.Error.Message != "Unsupported media type: text/plain; the body is taken as application/json, application/vnd.widget" {
		t.Errorf("text/plain: %d %s", rr.Code, rr.Body)
	}

	g := oapifly.New(oapifly.Config{Title: "test"})
	g.Register(reg.Endpoints()...)
	doc, err := json.Marshal(g.Generate())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(doc), `"400":`) {
		t.Errorf("the 400 is not described: %s", doc)
	}

	// BindBody binds and refuses as a registered request does.
	var bound createWidget
	var refusal *Error
	if err := BindBody(&bound, []byte(`{"name":"far too long a name"}`)); !errors.As(err, &refusal) ||
		refusal.Status != http.StatusUnprocessableEntity || refusal.Fields["name"] == "" {
		t.Errorf("BindBody past maxLength: %v", err)
	}
	if err := BindBody(&bound, []byte(`[]`)); !errors.As(err, &refusal) || refusal.Status != http.StatusUnprocessableEntity {
		t.Errorf("BindBody of an array: %v", err)
	}
	if err := BindBody(&bound, []byte(`{"name":"ok"}`)); err != nil || bound.Name != "ok" {
		t.Errorf("BindBody: %v %+v", err, bound)
	}
}
