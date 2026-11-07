package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"

	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/examplepairs"
)

// openSlowStatementsScratchDB gives a test a database of its own, dropped
// afterwards: pg_stat_statements is created per database, so one without it
// answers as an operator's database without the extension does.
func openSlowStatementsScratchDB(t *testing.T) *gorm.DB {
	t.Helper()
	return openScratchDB(t, "slowstmt", nil)
}

func slowStatementsRoutes(db *gorm.DB) http.Handler {
	routes := chi.NewRouter()
	NewSlowStatementsHandler(db).Register(endpoint.Routes{Router: routes, Registry: endpoint.NewRegistry()},
		func(string) endpoint.Middleware {
			return endpoint.Middleware{Wrap: func(next http.Handler) http.Handler { return next }}
		})
	return routes
}

func serveSlowStatements(t *testing.T, h http.Handler, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(method, target, nil))
	return rr
}

func slowStatementsPair(t *testing.T, op, name string) (int, json.RawMessage) {
	t.Helper()
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	pair, ok := pairs[op][name]
	if !ok {
		t.Fatalf("%s/%s pair missing", op, name)
	}
	body, err := json.Marshal(pair.Response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return pair.Response.Status, body
}

func assertSlowStatementsBody(t *testing.T, want json.RawMessage, got []byte) {
	t.Helper()
	var w, g any
	if err := json.Unmarshal(want, &w); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("answer is not JSON: %s", got)
	}
	wj, _ := json.Marshal(w)
	gj, _ := json.Marshal(g)
	if string(wj) != string(gj) {
		t.Fatalf("body\n got %s\nwant %s", gj, wj)
	}
}

// withoutTimes is a slow statement view's answer with each statement's times
// taken out.
func withoutTimes(t *testing.T, body []byte) []byte {
	t.Helper()
	var view struct {
		Data struct {
			Sort       string           `json:"sort"`
			Statements []map[string]any `json:"statements"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatalf("not a slow statement view: %s", body)
	}
	for _, s := range view.Data.Statements {
		for _, k := range []string{"total_ms", "mean_ms"} {
			if _, ok := s[k].(float64); !ok {
				t.Fatalf("%s is not a number: %s", k, body)
			}
			delete(s, k)
		}
	}
	out, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// A database without pg_stat_statements answers 503 statistics_unavailable on
// both operations, never a 500 (#1629).
func TestSlowStatements_UnavailableWithoutTheExtension(t *testing.T) {
	h := slowStatementsRoutes(openSlowStatementsScratchDB(t))

	t.Run("list", func(t *testing.T) {
		examplepairs.Proven(t, "listSlowStatements", "unavailable")
		status, body := slowStatementsPair(t, "listSlowStatements", "unavailable")
		rr := serveSlowStatements(t, h, http.MethodGet, "/api/v1/admin/database/statements")
		if rr.Code != status {
			t.Fatalf("got %d, want %d: %s", rr.Code, status, rr.Body)
		}
		assertSlowStatementsBody(t, body, rr.Body.Bytes())
	})
	t.Run("reset", func(t *testing.T) {
		examplepairs.Proven(t, "resetSlowStatements", "unavailable")
		status, body := slowStatementsPair(t, "resetSlowStatements", "unavailable")
		rr := serveSlowStatements(t, h, http.MethodPost, "/api/v1/admin/database/statements/reset")
		if rr.Code != status {
			t.Fatalf("got %d, want %d: %s", rr.Code, status, rr.Body)
		}
		assertSlowStatementsBody(t, body, rr.Body.Bytes())
	})
}

// With the library preloaded and the extension created as the boot creates it,
// a reset empties the view, a statement run afterwards shows in it normalised
// (its constant replaced, so no value reaches the answer), and both orders
// answer. It skips where the database does not preload pg_stat_statements.
func TestSlowStatements_ListAndReset(t *testing.T) {
	db := openSlowStatementsScratchDB(t)
	if err := db.Exec(`CREATE EXTENSION IF NOT EXISTS pg_stat_statements`).Error; err != nil {
		t.Skipf("slow statements test skipped: cannot create pg_stat_statements (%v)", err)
	}
	if err := db.Exec(`SELECT 1 FROM pg_stat_statements LIMIT 1`).Error; err != nil {
		var pg *pgconn.PgError
		if errors.As(err, &pg) && pg.Code == "55000" {
			t.Skip("slow statements test skipped: pg_stat_statements is not in shared_preload_libraries")
		}
		t.Fatal(err)
	}
	// One connection, so the reset and the reads below see each other in order.
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	h := slowStatementsRoutes(db)

	t.Run("reset", func(t *testing.T) {
		examplepairs.Proven(t, "resetSlowStatements", "reset")
		status, _ := slowStatementsPair(t, "resetSlowStatements", "reset")
		rr := serveSlowStatements(t, h, http.MethodPost, "/api/v1/admin/database/statements/reset")
		if rr.Code != status || rr.Body.Len() != 0 {
			t.Fatalf("got %d %q, want %d and no body", rr.Code, rr.Body, status)
		}
	})
	t.Run("only the reset after a reset", func(t *testing.T) {
		examplepairs.Proven(t, "listSlowStatements", "available")
		status, body := slowStatementsPair(t, "listSlowStatements", "available")
		rr := serveSlowStatements(t, h, http.MethodGet, "/api/v1/admin/database/statements")
		if rr.Code != status {
			t.Fatalf("got %d, want %d: %s", rr.Code, status, rr.Body)
		}
		// Times vary from run to run: the rest is held to the pair.
		assertSlowStatementsBody(t, withoutTimes(t, body), withoutTimes(t, rr.Body.Bytes()))
	})
	t.Run("normalised statements", func(t *testing.T) {
		const secret = "slow-statement-secret-value"
		if err := db.Exec(`SELECT pg_sleep(0.01), length('` + secret + `')`).Error; err != nil {
			t.Fatal(err)
		}
		for _, sort := range []string{"total", "mean"} {
			rr := serveSlowStatements(t, h, http.MethodGet, "/api/v1/admin/database/statements?sort="+sort+"&limit=100")
			if rr.Code != http.StatusOK {
				t.Fatalf("sort=%s: got %d: %s", sort, rr.Code, rr.Body)
			}
			if strings.Contains(rr.Body.String(), secret) {
				t.Fatalf("sort=%s: a constant reached the answer: %s", sort, rr.Body)
			}
			var got struct {
				Data SlowStatements `json:"data"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.Data.Sort != sort {
				t.Fatalf("sort = %q, want %q", got.Data.Sort, sort)
			}
			found := false
			for i, s := range got.Data.Statements {
				if i > 0 {
					prev, cur := got.Data.Statements[i-1], s
					if (sort == "total" && prev.TotalMS < cur.TotalMS) || (sort == "mean" && prev.MeanMS < cur.MeanMS) {
						t.Fatalf("sort=%s: not slowest first: %+v", sort, got.Data.Statements)
					}
				}
				if strings.Contains(s.Query, "pg_sleep($1), length($2)") {
					found = true
					if s.Calls != 1 || s.TotalMS < 10 || s.MeanMS < 10 || s.Rows != 1 {
						t.Fatalf("sort=%s: figures %+v", sort, s)
					}
				}
			}
			if !found {
				t.Fatalf("sort=%s: the slept statement is not listed: %+v", sort, got.Data.Statements)
			}
		}
		rr := serveSlowStatements(t, h, http.MethodGet, "/api/v1/admin/database/statements?limit=1")
		var one struct {
			Data SlowStatements `json:"data"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &one); err != nil || len(one.Data.Statements) != 1 {
			t.Fatalf("limit=1: %d %s", rr.Code, rr.Body)
		}
	})
}
