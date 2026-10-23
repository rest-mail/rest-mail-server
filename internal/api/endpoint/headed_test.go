package endpoint

import (
	"net/http"
	"net/http/httptest"
	"testing"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/go-chi/chi/v5"
)

type session struct {
	ExpiresIn int `json:"expires_in"`
}

// A Headed answer writes its headers, then its value as it would be on its own, and the
// operation's headers describe every success response.
func TestHeadedWritesItsHeadersAndItsValue(t *testing.T) {
	r := chi.NewRouter()
	reg := NewRegistry()
	cookies := map[string]oapifly.ResponseHeader{"Set-Cookie": {Description: "The session cookies"}}
	Register(Routes{Router: r, Registry: reg}, Operation{Method: http.MethodPost, Path: "/login", OperationID: "login",
		Headers: cookies}, func(*http.Request, struct{}) (Headed[session], error) {
		return Headed[session]{Header: http.Header{"Set-Cookie": {"a=1; HttpOnly", "b=2"}}, Value: session{ExpiresIn: 900}}, nil
	})
	Register(Routes{Router: r, Registry: reg}, Operation{Method: http.MethodPost, Path: "/logout", OperationID: "logout",
		Status: http.StatusNoContent, Headers: cookies}, func(*http.Request, struct{}) (Headed[Empty], error) {
		return Headed[Empty]{Header: http.Header{"Set-Cookie": {"a=; Max-Age=0"}}}, nil
	})
	Register(Routes{Router: r, Registry: reg}, Operation{Method: http.MethodPut, Path: "/upsert", OperationID: "upsert",
		Status: http.StatusOK, SuccessStatuses: []int{http.StatusCreated}},
		func(*http.Request, struct{}) (Headed[Status[session]], error) {
			return Headed[Status[session]]{Header: http.Header{"Location": {"/upsert/1"}},
				Value: Status[session]{Code: http.StatusCreated, Value: session{ExpiresIn: 1}}}, nil
		})

	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/login", nil))
	if rr.Code != http.StatusOK || rr.Body.String() != `{"data":{"expires_in":900}}`+"\n" {
		t.Fatalf("login = %d %q", rr.Code, rr.Body)
	}
	if got := rr.Header().Values("Set-Cookie"); len(got) != 2 || got[0] != "a=1; HttpOnly" || got[1] != "b=2" {
		t.Fatalf("cookies = %q", got)
	}

	rr = httptest.NewRecorder()
	r.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/logout", nil))
	if rr.Code != http.StatusNoContent || rr.Body.Len() != 0 || rr.Header().Get("Set-Cookie") != "a=; Max-Age=0" {
		t.Fatalf("logout = %d %q %v", rr.Code, rr.Body, rr.Header())
	}

	rr = httptest.NewRecorder()
	r.ServeHTTP(rr, httptest.NewRequest(http.MethodPut, "/upsert", nil))
	if rr.Code != http.StatusCreated || rr.Header().Get("Location") != "/upsert/1" {
		t.Fatalf("upsert = %d %v", rr.Code, rr.Header())
	}

	endpoints := reg.Endpoints()
	login := endpoints[0].Responses[0]
	if login.Status != http.StatusOK || login.Type != oapifly.TypeOf[Data[session]]() || login.Headers["Set-Cookie"].Description != "The session cookies" {
		t.Fatalf("login described as %+v", login)
	}
	if logout := endpoints[1].Responses[0]; logout.Status != http.StatusNoContent || logout.Type != nil || logout.Headers == nil {
		t.Fatalf("logout described as %+v", logout)
	}
	upsert := endpoints[2].Responses
	if upsert[0].Type != oapifly.TypeOf[Data[session]]() || upsert[1].Status != http.StatusCreated {
		t.Fatalf("upsert described as %+v", upsert)
	}
	for _, response := range upsert {
		if response.Status >= 400 && response.Headers != nil {
			t.Fatalf("an error response carries the success headers: %+v", response)
		}
	}
}
