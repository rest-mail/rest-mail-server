package handlers

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/restmail/restmail/internal/api/respond"
)

// Shared limit/offset pagination defaults. The maximum mirrors the hard cap the
// queue, delivery-log, activity-log and TLS-report endpoints already apply, so no
// list endpoint can be turned into an unbounded full-table scan.
const (
	defaultListLimit = 50
	maxListLimit     = 200
)

// validateIntegerQuery refuses malformed integer filters before they reach SQL
// or pagination's fallback logic. maxLimit is nonzero only when the operation
// documents a maximum; numeric values otherwise retain their existing handling.
func validateIntegerQuery(w http.ResponseWriter, r *http.Request, maxLimit int64, names ...string) bool {
	invalid := map[string]string{}
	for _, name := range names {
		value := r.URL.Query().Get(name)
		if value == "" {
			continue
		}
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			invalid[name] = "must be a whole number"
		} else if name == "limit" && maxLimit > 0 && n > maxLimit {
			invalid[name] = "exceeds the maximum page size"
		}
	}
	if len(invalid) > 0 {
		respond.ValidationError(w, invalid)
		return false
	}
	return true
}

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
