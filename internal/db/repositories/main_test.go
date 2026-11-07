package repositories

import (
	"os"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/auth"
	"gorm.io/gorm"
)

// testPasswordCost is what this package's tests hash passwords at: bcrypt is slow
// on purpose, and many times slower under the race detector CI runs with. It is
// above bcrypt's lowest, so a test can still store a hash cheaper than the
// server's. internal/auth proves the real cost, and cmd/console a real upgrade to it.
const testPasswordCost = 5

func TestMain(m *testing.M) {
	auth.LowerPasswordCostForTests(testPasswordCost)
	os.Exit(m.Run())
}

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
