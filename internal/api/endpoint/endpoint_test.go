package endpoint

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/go-chi/chi/v5"

	"github.com/restmail/restmail/internal/api/respond"
)

type widget struct {
	ID   uint64 `json:"id"`
	Name string `json:"name"`
}

type widgetPath struct {
	ID uint64 `path:"id"`
}

type widgetQuery struct {
	Limit *int   `query:"limit"`
	Trace string `header:"X-Trace"`
}

type createWidget struct {
	Name string `json:"name" maxLength:"8"`
}

type tagged struct {
	Tags   []string          `json:"tags"`
	Labels map[string]string `json:"labels"`
}

type renameWidget struct {
	ID   uint64       `path:"id"`
	Body createWidget `body:""`
}

func server(t *testing.T) (*chi.Mux, *Registry) {
	t.Helper()
	r := chi.NewRouter()
	reg := NewRegistry()
	blocked := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if req.Header.Get("X-Block") != "" {
				respond.Error(w, http.StatusForbidden, "forbidden", "Blocked")
				return
			}
			next.ServeHTTP(w, req)
		})
	}

	Register(r, reg, Operation{Method: http.MethodGet, Path: "/widgets/{id:[0-9]+}", OperationID: "getWidget",
		Errors: []int{http.StatusNotFound}, Middleware: []func(http.Handler) http.Handler{blocked}},
		func(_ *http.Request, in widgetPath) (widget, error) {
			switch in.ID {
			case 7:
				return widget{ID: 7, Name: "seven"}, nil
			case 8:
				return widget{}, errors.New("database down")
			}
			return widget{}, Refuse(http.StatusNotFound, "not_found", "Widget not found")
		})
	Register(r, reg, Operation{Method: http.MethodGet, Path: "/widgets", OperationID: "listWidgets"},
		func(_ *http.Request, in widgetQuery) (List[widget], error) {
			n := 2
			if in.Limit != nil {
				n = *in.Limit
			}
			out := List[widget]{Data: []widget{}, Pagination: &respond.Pagination{HasMore: false, Total: int64(n)}}
			for i := 0; i < n; i++ {
				out.Data = append(out.Data, widget{ID: uint64(i + 1), Name: in.Trace})
			}
			return out, nil
		})
	// The API's body cap, which reading the body runs into.
	capped := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			req.Body = http.MaxBytesReader(w, req.Body, 1024)
			next.ServeHTTP(w, req)
		})
	}
	Register(r, reg, Operation{Method: http.MethodPost, Path: "/widgets", OperationID: "createWidget",
		Status: http.StatusCreated, Middleware: []func(http.Handler) http.Handler{capped}},
		func(_ *http.Request, in createWidget) (widget, error) {
			if in.Name == "" {
				return widget{}, Invalid(map[string]string{"name": "required"})
			}
			return widget{ID: 9, Name: in.Name}, nil
		})
	Register(r, reg, Operation{Method: http.MethodPatch, Path: "/widgets/{id}", OperationID: "renameWidget"},
		func(_ *http.Request, in renameWidget) (widget, error) {
			return widget{ID: in.ID, Name: in.Body.Name}, nil
		})
	Register(r, reg, Operation{Method: http.MethodGet, Path: "/empty", OperationID: "listNothing"},
		func(_ *http.Request, _ Empty) (List[widget], error) { return List[widget]{}, nil })
	Register(r, reg, Operation{Method: http.MethodGet, Path: "/tagged", OperationID: "getTagged"},
		func(_ *http.Request, _ Empty) (tagged, error) { return tagged{}, nil })
	Register(r, reg, Operation{Method: http.MethodDelete, Path: "/widgets/{id}", OperationID: "deleteWidget",
		Status: http.StatusNoContent},
		func(_ *http.Request, _ widgetPath) (Empty, error) { return Empty{}, nil })
	return r, reg
}

func call(t *testing.T, h http.Handler, method, target, body string, headers ...string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, strings.TrimSpace(rec.Body.String())
}

// What a handler returns is written in respond's envelope, with the operation's status.
func TestRegisteredRoutesAnswer(t *testing.T) {
	r, _ := server(t)
	cases := []struct {
		method, target, body string
		headers              []string
		status               int
		want                 string
	}{
		{"GET", "/widgets/7", "", nil, 200, `{"data":{"id":7,"name":"seven"}}`},
		{"GET", "/widgets/9", "", nil, 404, `{"error":{"code":"not_found","message":"Widget not found"}}`},
		{"GET", "/widgets/8", "", nil, 500, `{"error":{"code":"internal_error","message":"Internal server error"}}`},
		{"GET", "/widgets/7", "", []string{"X-Block", "1"}, 403, `{"error":{"code":"forbidden","message":"Blocked"}}`},
		{"GET", "/widgets?limit=1", "", []string{"X-Trace", "t1"}, 200, `{"data":[{"id":1,"name":"t1"}],"pagination":{"has_more":false,"total":1}}`},
		// The request type's rules are enforced before the handler runs, field by field.
		{"GET", "/widgets?limit=x", "", nil, 422, `{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"limit":"must be an integer"}}}}`},
		{"POST", "/widgets", `{"name":"far too long"}`, nil, 422, `{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"name":"must be at most 8 characters"}}}}`},
		{"POST", "/widgets", `{"name":"n","colour":"red"}`, nil, 422, `{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"colour":"not allowed"}}}}`},
		{"POST", "/widgets", `{"name":"new"}`, nil, 201, `{"data":{"id":9,"name":"new"}}`},
		{"POST", "/widgets", `{"name":`, nil, 400, `{"error":{"code":"bad_request","message":"Invalid request body"}}`},
		{"POST", "/widgets", `{}`, nil, 422, `{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"name":"required"}}}}`},
		// The handler's own validation still answers the same way.
		{"POST", "/widgets", `{"name":""}`, nil, 422, `{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"name":"required"}}}}`},
		{"POST", "/widgets", strings.Repeat(" ", 2048) + `{"name":"n"}`, nil, 413, `{"error":{"code":"request_too_large","message":"Request body too large"}}`},
		{"PATCH", "/widgets/3", `{"name":"three"}`, nil, 200, `{"data":{"id":3,"name":"three"}}`},
		{"PATCH", "/widgets/3", `nope`, nil, 400, `{"error":{"code":"bad_request","message":"Invalid request body"}}`},
		{"DELETE", "/widgets/3", "", nil, 204, ``},
		// A list the handler left nil is written as [], a map as {}.
		{"GET", "/empty", "", nil, 200, `{"data":[]}`},
		{"GET", "/tagged", "", nil, 200, `{"data":{"tags":[],"labels":{}}}`},
	}
	for _, c := range cases {
		status, body := call(t, r, c.method, c.target, c.body, c.headers...)
		if status != c.status || body != c.want {
			t.Errorf("%s %s: %d %s, want %d %s", c.method, c.target, status, body, c.status, c.want)
		}
	}
}

// The registrations are the document: every route is described, with its success in the
// envelope it is written in, and its errors in respond's error envelope.
func TestRegistrationsAreTheDocument(t *testing.T) {
	_, reg := server(t)
	g := oapifly.New(oapifly.Config{Title: "test"})
	g.Register(reg.Endpoints()...)
	spec := g.Generate()
	if len(g.Warnings) > 0 {
		t.Fatalf("the registrations should describe cleanly: %v", g.Warnings)
	}
	doc, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Paths map[string]map[string]struct {
			OperationID string `json:"operationId"`
			Parameters  []struct {
				Name string `json:"name"`
				In   string `json:"in"`
			} `json:"parameters"`
			Responses map[string]struct {
				Content map[string]struct {
					Schema map[string]interface{} `json:"schema"`
				} `json:"content"`
			} `json:"responses"`
		} `json:"paths"`
		Components struct {
			Schemas map[string]interface{} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(doc, &parsed); err != nil {
		t.Fatal(err)
	}

	get := parsed.Paths["/widgets/{id}"]["get"]
	if get.OperationID != "getWidget" {
		t.Fatalf("a chi pattern is documented as its template: %v", parsed.Paths)
	}
	if len(get.Parameters) != 1 || get.Parameters[0].Name != "id" || get.Parameters[0].In != "path" {
		t.Fatalf("the path parameter: %+v", get.Parameters)
	}
	for _, status := range []string{"200", "404", "422", "500"} {
		if _, ok := get.Responses[status]; !ok {
			t.Fatalf("getWidget answers %s: %v", status, get.Responses)
		}
	}
	if _, ok := get.Responses["400"]; ok {
		t.Fatalf("getWidget has no body to be malformed: %v", get.Responses)
	}
	create := parsed.Paths["/widgets"]["post"]
	for _, status := range []string{"201", "400", "413", "422", "500"} {
		if _, ok := create.Responses[status]; !ok {
			t.Fatalf("createWidget answers %s: %v", status, create.Responses)
		}
	}
	if ref := get.Responses["200"].Content["application/json"].Schema["$ref"]; ref != "#/components/schemas/Data-widget" {
		t.Fatalf("the success is the data envelope: %v", get.Responses["200"])
	}
	if ref := get.Responses["404"].Content["application/json"].Schema["$ref"]; ref != "#/components/schemas/ErrorResponse" {
		t.Fatalf("an error is respond's error envelope: %v", get.Responses["404"])
	}

	list := parsed.Paths["/widgets"]["get"]
	if ref := list.Responses["200"].Content["application/json"].Schema["$ref"]; ref != "#/components/schemas/List-widget" {
		t.Fatalf("a list is its own envelope: %v", list.Responses["200"])
	}
	if del := parsed.Paths["/widgets/{id}"]["delete"].Responses["204"]; del.Content != nil {
		t.Fatalf("a 204 has no body: %+v", del)
	}
	for _, name := range []string{"Data-widget", "List-widget", "widget", "ErrorResponse", "Pagination"} {
		if _, ok := parsed.Components.Schemas[name]; !ok {
			t.Fatalf("component %s missing: %v", name, parsed.Components.Schemas)
		}
	}
	if !reg.Has("GET", "/widgets/{id:[0-9]+}") || reg.Has("PUT", "/widgets/{id}") {
		t.Fatalf("Has reports what was registered")
	}
	if routes := strings.Join(reg.Routes(), ","); !strings.Contains(routes, "GET /widgets/{id:[0-9]+},") || !strings.HasPrefix(routes, "DELETE /widgets/{id},") {
		t.Fatalf("Routes lists the patterns as mounted, sorted: %s", routes)
	}
}

// A route registered twice is a programming error, caught at start-up.
func TestRegisterTwicePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("registering a route twice must panic")
		}
	}()
	r := chi.NewRouter()
	reg := NewRegistry()
	h := func(*http.Request, Empty) (Empty, error) { return Empty{}, nil }
	Register(r, reg, Operation{Method: "GET", Path: "/a"}, h)
	Register(r, reg, Operation{Method: "GET", Path: "/a"}, h)
}
