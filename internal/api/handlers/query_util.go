package handlers

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

)

// errZeroID is pathID's refusal of an ID of 0.
var errZeroID = errors.New("an ID starts at 1")

// pathID parses the row ID in path parameter name. The spec's path IDs have a
// minimum of 1 (#388) and no row has ID 0, so 0 is refused like a malformed ID:
// otherwise a delete by ID 0 matches nothing and still reports success.
func pathID(r *http.Request, name string) (uint64, error) {
	id, err := strconv.ParseUint(chi.URLParam(r, name), 10, 32)
	if err == nil && id == 0 {
		return 0, errZeroID
	}
	return id, err
}

// Shared limit/offset pagination defaults. The maximum mirrors the hard cap the
// queue, delivery-log, activity-log and TLS-report endpoints already apply, so no
// list endpoint can be turned into an unbounded full-table scan.
const (
	defaultListLimit = 50
	maxListLimit     = 200
)

// parsePagination reads the shared `limit`/`offset` query parameters and applies a
// default page size plus a hard maximum. limit defaults to defLimit and is clamped
// into [1, maxLimit]; offset defaults to 0 and is floored at 0. A missing,
// non-numeric, negative or over-cap value can therefore never widen the result set
// beyond maxLimit rows.
func parsePagination(r *http.Request, defLimit, maxLimit int) (limit, offset int) {
	limit = defLimit
	if l, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && l > 0 {
		if l > maxLimit {
			l = maxLimit
		}
		limit = l
	}
	offset = 0
	if o, err := strconv.Atoi(r.URL.Query().Get("offset")); err == nil && o > 0 {
		offset = o
	}
	return limit, offset
}

// pagination is parsePagination for a typed request's limit and offset: an absent or
// non-positive limit is the default, one over maxLimit is maxLimit, and an absent or negative
// offset is 0.
func pagination(limit, offset *int, defLimit, maxLimit int) (int, int) {
	l, o := defLimit, 0
	if limit != nil && *limit > 0 {
		l = min(*limit, maxLimit)
	}
	if offset != nil && *offset > 0 {
		o = *offset
	}
	return l, o
}

// likeEscaper escapes the three LIKE/ILIKE metacharacters. It is safe to run once:
// strings.Replacer does not re-scan its own output, so escaping the escape
// character first does not double-escape the wildcards.
var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// escapeLike escapes the LIKE/ILIKE wildcard metacharacters (`%`, `_`) and the
// escape character (`\`) in a user-supplied search term so they match literally
// instead of changing the filter semantics. Callers must pair it with an explicit
// `ESCAPE '\'` clause on the query (the parameter value is already parameterized,
// so this is a semantic fix, not an injection fix).
func escapeLike(s string) string {
	return likeEscaper.Replace(s)
}
