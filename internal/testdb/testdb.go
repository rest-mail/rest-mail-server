// Package testdb holds what tests share about their database: what a test does
// when it does not answer, and how a test's own pool behaves.
package testdb

import (
	"os"
	"testing"

	"gorm.io/gorm"
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

// ReuseConnections keeps two of a test's connections open between statements and
// closes the pool when the test ends. A config.Config built by hand keeps none, so
// every statement outside a transaction connects and authenticates all over again.
//
// Call it straight after opening the pool, so the close runs after the test's other
// cleanups, and only on a pool that belongs to this one test.
func ReuseConnections(t testing.TB, db *gorm.DB) {
	t.Helper()
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	pool.SetMaxIdleConns(2)
	t.Cleanup(func() { _ = pool.Close() })
}
