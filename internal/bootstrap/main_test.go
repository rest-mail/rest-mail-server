package bootstrap

import (
	"os"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/auth"
)

func TestMain(m *testing.M) {
	// bcrypt is slow on purpose. Nothing here checks a hash's cost; internal/auth
	// proves the cost production uses.
	auth.UseMinimumHashCostInTests()
	os.Exit(m.Run())
}
