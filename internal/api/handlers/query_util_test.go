package handlers

import (
	"net/http/httptest"
	"testing"
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
