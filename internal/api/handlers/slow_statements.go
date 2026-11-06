package handlers

import (
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"

	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/api/middleware"
)

// The slow statement view gives an operator the database's slowest statements
// without a shell in the database pod (#1629). It reads pg_stat_statements,
// which the chart's Postgres preloads and the boot creates. Postgres
// normalises each statement, putting $1, $2 ... in place of constants, so no
// parameter value reaches the answer. A database without the extension (an
// operator's own managed Postgres that does not preload it) answers 503 with
// code statistics_unavailable, never a 500.

const slowStatementsDescription = "The current database's statements from pg_stat_statements, slowest first: by total execution time (`sort=total`, the default) or by mean time per call (`sort=mean`), at most `limit` of them (default 20, at most 100). Each is the statement as Postgres normalises it, constants replaced by `$1`, `$2` and so on, so no parameter value is shown, with its calls, total and mean execution time in milliseconds and the rows it returned or changed. The figures count from the last reset (`POST /api/v1/admin/database/statements/reset`) or the database's start. Answers 503 with code `statistics_unavailable` when the database does not have pg_stat_statements loaded: Postgres needs `shared_preload_libraries = 'pg_stat_statements'` and the extension created, which the chart's bundled Postgres has."

const slowStatementsResetDescription = "Resets the current database's pg_stat_statements counters, so the slow statement view counts from now: reset after a change, wait for traffic, then read the view. Answers 503 with code `statistics_unavailable` when the database does not have pg_stat_statements loaded or its user may not reset it."

const slowStatementsDefaultLimit = 20

var errStatisticsUnavailable = endpoint.Refuse(http.StatusServiceUnavailable, "statistics_unavailable",
	"The database does not have pg_stat_statements loaded")

var errStatisticsResetRefused = endpoint.Refuse(http.StatusServiceUnavailable, "statistics_unavailable",
	"The database user may not reset pg_stat_statements")

type SlowStatementsHandler struct{ db *gorm.DB }

func NewSlowStatementsHandler(db *gorm.DB) *SlowStatementsHandler {
	return &SlowStatementsHandler{db: db}
}

func (h *SlowStatementsHandler) Register(rs endpoint.Routes, at func(string) endpoint.Middleware) {
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/v1/admin/database/statements",
		OperationID: "listSlowStatements", Summary: "List the database's slowest statements", Description: slowStatementsDescription,
		Tags: []string{"Admin: Stats"}, Errors: []int{http.StatusServiceUnavailable},
		Middleware: []endpoint.Middleware{at(middleware.CapDatabaseRead)}}, h.list)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/api/v1/admin/database/statements/reset",
		OperationID: "resetSlowStatements", Summary: "Reset the database's statement statistics", Description: slowStatementsResetDescription,
		Tags: []string{"Admin: Stats"}, Status: http.StatusNoContent, Errors: []int{http.StatusServiceUnavailable},
		Middleware: []endpoint.Middleware{at(middleware.CapDatabaseManage)}}, h.reset)
}

type slowStatementsInput struct {
	Sort  *string `query:"sort" enums:"total,mean" message:"must be total or mean" description:"total orders by total execution time, mean by mean time per call; total when left out."`
	Limit *int    `query:"limit" minimum:"1" maximum:"100" description:"How many statements to list; 20 when left out."`
}

// SlowStatements is the slow statement view: the order asked for and the
// statements in it.
type SlowStatements struct {
	Sort       string          `json:"sort" description:"total or mean: what the statements are ordered by, slowest first"`
	Statements []SlowStatement `json:"statements"`
}

// SlowStatement is one normalised statement's figures since the last reset.
type SlowStatement struct {
	Query   string  `json:"query" description:"The statement as Postgres normalises it, constants replaced by $1, $2 and so on"`
	Calls   int64   `json:"calls"`
	TotalMS float64 `json:"total_ms" description:"Total execution time in milliseconds"`
	MeanMS  float64 `json:"mean_ms" description:"Mean execution time per call in milliseconds"`
	Rows    int64   `json:"rows" description:"Rows returned or changed, over every call"`
}

// The ORDER BY column comes from this map, never from the request text.
var slowStatementsOrder = map[string]string{"total": "total_exec_time", "mean": "mean_exec_time"}

func (h *SlowStatementsHandler) list(r *http.Request, in slowStatementsInput) (SlowStatements, error) {
	sort := "total"
	if in.Sort != nil {
		sort = *in.Sort
	}
	limit := slowStatementsDefaultLimit
	if in.Limit != nil {
		limit = *in.Limit
	}
	out := SlowStatements{Sort: sort, Statements: []SlowStatement{}}
	err := h.db.WithContext(r.Context()).Raw(`SELECT query, calls, total_exec_time AS total_ms, mean_exec_time AS mean_ms, rows
		FROM pg_stat_statements
		WHERE dbid = (SELECT oid FROM pg_database WHERE datname = current_database())
		ORDER BY `+slowStatementsOrder[sort]+` DESC, queryid
		LIMIT ?`, limit).Scan(&out.Statements).Error
	if err != nil {
		if statisticsMissing(err) {
			return SlowStatements{}, errStatisticsUnavailable
		}
		return SlowStatements{}, err
	}
	return out, nil
}

func (h *SlowStatementsHandler) reset(r *http.Request, _ struct{}) (endpoint.Empty, error) {
	// Only the current database's entries: other databases on a shared server
	// keep theirs.
	err := h.db.WithContext(r.Context()).Exec(`SELECT pg_stat_statements_reset(0, (SELECT oid FROM pg_database WHERE datname = current_database()), 0)`).Error
	if err != nil {
		if statisticsMissing(err) {
			return endpoint.Empty{}, errStatisticsUnavailable
		}
		var pg *pgconn.PgError
		if errors.As(err, &pg) && pg.Code == "42501" {
			return endpoint.Empty{}, errStatisticsResetRefused
		}
		return endpoint.Empty{}, err
	}
	return endpoint.Empty{}, nil
}

// statisticsMissing reports the errors Postgres gives when pg_stat_statements
// is not there: the view or function does not exist (42P01, 42883), or the
// extension exists but the library was not preloaded (55000).
func statisticsMissing(err error) bool {
	var pg *pgconn.PgError
	if !errors.As(err, &pg) {
		return false
	}
	switch pg.Code {
	case "42P01", "42883", "55000":
		return true
	}
	return false
}
