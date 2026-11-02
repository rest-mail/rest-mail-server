// Package testdb decides what a test does when its database does not answer.
package testdb

import (
	"os"
	"testing"
)

// Unreachable ends a test that could not reach its database. Setting DB_HOST is
// asking for these tests to run, so a database that does not answer is a failure:
// a skip would let a mistyped host or a stopped server read as a passing run.
// Without DB_HOST the test is optional, and skips.
func Unreachable(t testing.TB, format string, args ...any) {
	t.Helper()
	if os.Getenv("DB_HOST") != "" {
		t.Fatalf(format, args...)
	}
	t.Skipf(format, args...)
}
