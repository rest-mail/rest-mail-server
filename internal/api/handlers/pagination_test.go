package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/db/models"
)

// listEnvelope mirrors respond.List's wire shape for decoding paginated list
// responses in tests.
type listEnvelope struct {
	Data       []json.RawMessage `json:"data"`
	Pagination *struct {
		HasMore bool  `json:"has_more"`
		Total   int64 `json:"total"`
	} `json:"pagination"`
}

// TestDomainList_Pagination proves the previously-unbounded domain list endpoint
// now honours limit/offset and reports pagination metadata (issue #202: unbounded
// list endpoints). It is representative of the capped-pagination change applied to
// every bare-Find list handler.
func TestDomainList_Pagination(t *testing.T) {
	gdb := openAuthzTestDB(t)
	tx := gdb.Begin()
	t.Cleanup(func() { tx.Rollback() })

	// Clean slate within the transaction so the count is deterministic.
	if err := tx.Exec("DELETE FROM domains").Error; err != nil {
		t.Fatalf("clear domains: %v", err)
	}

	suffix := time.Now().UnixNano()
	// Names chosen so name-ASC order is a, b, c.
	for _, p := range []string{"a", "b", "c"} {
		name := fmt.Sprintf("%s-%d.test", p, suffix)
		if err := tx.Create(&models.Domain{Name: name, Active: true}).Error; err != nil {
			t.Fatalf("seed domain %s: %v", name, err)
		}
	}

	h := NewDomainHandler(tx, nil)

	list := func(query string) listEnvelope {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/domains?"+query, nil)
		rr := httptest.NewRecorder()
		h.List(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("List(%q) status = %d, want 200 (body %s)", query, rr.Code, rr.Body.String())
		}
		var env listEnvelope
		if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
			t.Fatalf("decode list body: %v", err)
		}
		return env
	}

	// limit honoured: first page of 2 of 3.
	page := list("limit=2")
	if len(page.Data) != 2 {
		t.Fatalf("limit=2 returned %d rows, want 2", len(page.Data))
	}
	if page.Pagination == nil || page.Pagination.Total != 3 || !page.Pagination.HasMore {
		t.Fatalf("limit=2 pagination = %+v, want total=3 has_more=true", page.Pagination)
	}

	// offset honoured: second page has the remaining 1 row, no more.
	page2 := list("limit=2&offset=2")
	if len(page2.Data) != 1 {
		t.Fatalf("limit=2&offset=2 returned %d rows, want 1", len(page2.Data))
	}
	if page2.Pagination == nil || page2.Pagination.HasMore {
		t.Fatalf("second page pagination = %+v, want has_more=false", page2.Pagination)
	}

	// default (no params): all 3 rows (well under the cap), no next page.
	all := list("")
	if len(all.Data) != 3 {
		t.Fatalf("default page returned %d rows, want 3", len(all.Data))
	}
	if all.Pagination == nil || all.Pagination.Total != 3 || all.Pagination.HasMore {
		t.Fatalf("default pagination = %+v, want total=3 has_more=false", all.Pagination)
	}
}
