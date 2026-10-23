package endpoint

import (
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// An upsert may answer 201 or 200 with the same body. Both statuses must describe
// the value that is actually written, without exposing the status wrapper.
func TestStatusResponse(t *testing.T) {
	r, reg := chi.NewRouter(), NewRegistry()
	Register(Routes{Router: r, Registry: reg}, Operation{
		Method: http.MethodPost, Path: "/upsert", Status: http.StatusCreated,
		SuccessStatuses: []int{http.StatusOK},
	}, func(req *http.Request, _ Empty) (Status[widget], error) {
		code := http.StatusCreated
		switch req.URL.Query().Get("existing") {
		case "true":
			code = http.StatusOK
		case "undeclared":
			code = http.StatusAccepted
		}
		return Status[widget]{Code: code, Value: widget{ID: 7, Name: "seven"}}, nil
	})
	for _, query := range []string{"", "?existing=true"} {
		code, body := call(t, r, http.MethodPost, "/upsert"+query, "")
		want := http.StatusCreated
		if query != "" {
			want = http.StatusOK
		}
		if code != want || body != `{"data":{"id":7,"name":"seven"}}` {
			t.Fatalf("%s: %d %s", query, code, body)
		}
	}
	ep := reg.Endpoints()[0]
	for _, code := range []int{http.StatusOK, http.StatusCreated} {
		found := false
		for _, response := range ep.Responses {
			if response.Status == code {
				found = true
				if response.Type != reflect.TypeOf(Data[widget]{}) {
					t.Fatalf("%d describes %v", code, response.Type)
				}
			}
		}
		if !found {
			t.Fatalf("missing success %d", code)
		}
	}
	code, body := call(t, r, http.MethodPost, "/upsert?existing=undeclared", "")
	if code != http.StatusInternalServerError || !strings.Contains(body, `"code":"internal_error"`) {
		t.Fatalf("undeclared status: %d %s", code, body)
	}
}

func TestMissingFieldKeepsDocumentedMessage(t *testing.T) {
	type request struct {
		IP string `json:"ip" minLength:"1" message:"IP address is required"`
	}
	r := chi.NewRouter()
	Register(Routes{Router: r, Registry: NewRegistry()}, Operation{Method: http.MethodPost, Path: "/ban"}, func(*http.Request, request) (Empty, error) {
		t.Fatal("invalid request reached handler")
		return Empty{}, nil
	})
	for _, body := range []string{`{}`, `{"ip":""}`} {
		code, got := call(t, r, http.MethodPost, "/ban", body)
		if code != 422 || got != `{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"ip":"IP address is required"}}}}` {
			t.Fatalf("%s: %d %s", body, code, got)
		}
	}
}
