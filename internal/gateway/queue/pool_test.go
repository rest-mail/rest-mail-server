package queue

import (
	"testing"

	"gorm.io/gorm"
)

// reuseConnections keeps two of a test's connections open between statements and
// closes the pool with the test. A config.Config left at zero keeps none, so every
// statement outside a transaction connects and authenticates all over again.
func reuseConnections(t *testing.T, gdb *gorm.DB) {
	t.Helper()
	pool, err := gdb.DB()
	if err != nil {
		t.Fatal(err)
	}
	pool.SetMaxIdleConns(2)
	t.Cleanup(func() { _ = pool.Close() })
}
