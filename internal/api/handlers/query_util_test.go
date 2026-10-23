package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

// TestParsePagination covers the shared limit/offset clamping used by the list
// endpoints: a valid value is honoured, an over-cap value is clamped to the
// maximum, a missing/zero/negative/garbage value falls back to the default, and a
// negative offset is floored at 0. This is the guard that stops a client widening
// a list into a full-table scan (issue #202: unbounded list endpoints).
func TestParsePagination(t *testing.T) {
	const def, max = 50, 200

	cases := []struct {
		name       string
		query      string
		wantLimit  int
		wantOffset int
	}{
		{"defaults when absent", "", def, 0},
		{"limit honoured", "limit=25", 25, 0},
		{"limit clamped to max", "limit=9999", max, 0},
		{"limit at cap honoured", "limit=200", 200, 0},
		{"zero limit falls back to default", "limit=0", def, 0},
		{"negative limit falls back to default", "limit=-5", def, 0},
		{"garbage limit falls back to default", "limit=abc", def, 0},
		{"offset honoured", "offset=40", def, 40},
		{"negative offset floored", "offset=-10", def, 0},
		{"limit and offset together", "limit=10&offset=30", 10, 30},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/x?"+tc.query, nil)
			limit, offset := parsePagination(req, def, max)
			if limit != tc.wantLimit {
				t.Errorf("limit = %d, want %d", limit, tc.wantLimit)
			}
			if offset != tc.wantOffset {
				t.Errorf("offset = %d, want %d", offset, tc.wantOffset)
			}
		})
	}
}

// TestPathID covers the shared path ID parser: the spec's path IDs have a
// minimum of 1 (#388), so 0, a negative number, a fraction, a word and a value
// past 32 bits are refused, and an ID from 1 up is returned.
func TestPathID(t *testing.T) {
	cases := []struct {
		value  string
		want   uint64
		wantOK bool
	}{
		{"1", 1, true},
		{"42", 42, true},
		{"4294967295", 4294967295, true},
		{"0", 0, false},
		{"-1", 0, false},
		{"1.5", 0, false},
		{"abc", 0, false},
		{"", 0, false},
		{"4294967296", 0, false},
	}

	for _, tc := range cases {
		t.Run(tc.value, func(t *testing.T) {
			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("id", tc.value)
			req := httptest.NewRequest("GET", "/x", nil)
			req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

			got, err := pathID(req, "id")
			if (err == nil) != tc.wantOK {
				t.Fatalf("pathID(%q) error = %v, want ok %v", tc.value, err, tc.wantOK)
			}
			if tc.wantOK && got != tc.want {
				t.Errorf("pathID(%q) = %d, want %d", tc.value, got, tc.want)
			}
		})
	}
}

// TestDeleteByIDZeroRefused pins the probe #388 found: a delete by ID 0 matched
// no row and still answered success. It is refused before the database (the
// handlers have none here), as the schema's minimum of 1 says.
func TestDeleteByIDZeroRefused(t *testing.T) {
	handlers := map[string]http.HandlerFunc{
		"deleteBan":          banRoutes(&BanHandler{}).ServeHTTP,
		"deletePipeline":     (&PipelineHandler{}).DeletePipeline,
		"deleteCustomFilter": (&PipelineHandler{}).DeleteCustomFilter,
	}
	for name, h := range handlers {
		t.Run(name, func(t *testing.T) {
			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("id", "0")
			path := "/x/0"
			if name == "deleteBan" {
				path = "/api/v1/admin/bans/0"
			}
			req := httptest.NewRequest("DELETE", path, nil)
			req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
			rec := httptest.NewRecorder()

			h(rec, req)
			want := http.StatusBadRequest
			if name == "deleteBan" {
				want = http.StatusUnprocessableEntity
			}
			if rec.Code != want {
				t.Errorf("status = %d, want %d", rec.Code, want)
			}
		})
	}
}

// TestEscapeLike proves the LIKE metacharacters and the escape character are
// neutralised so a search term matches literally (issue #202: LIKE wildcard
// injection). The wildcards %, _ and the escape \ must be prefixed with a
// backslash; ordinary characters are left untouched.
func TestEscapeLike(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"plain", "plain"},
		{"50%", `50\%`},
		{"a_b", `a\_b`},
		{`c\d`, `c\\d`},
		{`%_\`, `\%\_\\`},
		{"", ""},
		{"no-meta@example.com", "no-meta@example.com"},
	}
	for _, tc := range cases {
		if got := escapeLike(tc.in); got != tc.want {
			t.Errorf("escapeLike(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
