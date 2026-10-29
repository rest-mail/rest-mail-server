package api

import (
	"bytes"
	"github.com/restmail/restmail/internal/examplepairs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const (
	messageSearchPath = "/api/v1/accounts/{id}/search"
	messageUpdatePath = "/api/v1/messages/{id}"
)

// The refusals of searching and updating mail, sent through the production router.
// A found search needs PostgreSQL full-text matching and a changed message a stored
// row to write, so those pairs are left to fixtures that model them.
func TestMessageSearchRefusalExamples(t *testing.T) {
	scenarios := messageCollectionReadScenarios(t)
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"invalid_id", "missing"} {
		pair, ok := pairs["searchMessages"][name]
		if !ok {
			t.Fatalf("missing searchMessages pair %s", name)
		}
		t.Run(name, func(t *testing.T) {
			examplepairs.Proven(t, "searchMessages", name)
			s := scenarios[name]
			req := httptest.NewRequest(http.MethodGet, messageCollectionReadURI(messageSearchPath, pair), nil)
			req.Header.Set("Authorization", "Bearer "+s.token)
			rr := httptest.NewRecorder()
			s.handler.ServeHTTP(rr, req)
			if rr.Code != pair.Response.Status || rr.Header().Get("Content-Type") != pair.Response.MediaType {
				t.Fatalf("got %d %s %s, want %d %s", rr.Code, rr.Header().Get("Content-Type"), rr.Body.String(), pair.Response.Status, pair.Response.MediaType)
			}
			assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
		})
	}
}

func TestMessageUpdateRefusalExamples(t *testing.T) {
	f := newMessageRemovalScenarios(t)
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"invalid_id", "body_array", "missing"} {
		pair, ok := pairs["updateMessage"][name]
		if !ok {
			t.Fatalf("missing updateMessage pair %s", name)
		}
		t.Run(name, func(t *testing.T) {
			examplepairs.Proven(t, "updateMessage", name)
			s := f.scenarios["found"]
			before := len(f.store.snapshot())
			req := httptest.NewRequest(http.MethodPatch, messageCollectionReadURI(messageUpdatePath, pair), bytes.NewReader(pair.Request.Body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+s.token)
			rr := httptest.NewRecorder()
			s.handler.ServeHTTP(rr, req)
			if rr.Code != pair.Response.Status || rr.Header().Get("Content-Type") != pair.Response.MediaType {
				t.Fatalf("got %d %s %s, want %d %s", rr.Code, rr.Header().Get("Content-Type"), rr.Body.String(), pair.Response.Status, pair.Response.MediaType)
			}
			assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
			for _, call := range f.store.snapshot()[before:] {
				if strings.HasPrefix(call.query, "UPDATE") {
					t.Fatalf("refused update wrote: %s", call.query)
				}
			}
		})
	}
}
