package api

import (
	"fmt"
	"os"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/examplepairs"
	"github.com/rest-mail/rest-mail-server/internal/push"
)

// TestMain writes the example pairs this package's tests proved, when CI asks for
// them (examplepairs.ProofsEnv), so a check can show each was proven (#1237),
// and drops the migrated template database the scratch databases copy. Routers
// built here keep their push routes but start no dispatcher; internal/push tests
// the dispatcher itself. Fixtures hash at the lowest bcrypt cost: nothing here
// depends on the cost, internal/auth proves the production one, and at that cost
// the session fixtures alone took a shard past its timeout (#1942).
func TestMain(m *testing.M) {
	auth.UseMinimumHashCostInTests()
	startPushDispatcher = func(*push.Dispatcher) {}
	code := m.Run()
	dropMigratedTemplate()
	if err := examplepairs.WriteProofs(); err != nil {
		fmt.Fprintln(os.Stderr, "example proofs:", err)
		code = 1
	}
	os.Exit(code)
}
