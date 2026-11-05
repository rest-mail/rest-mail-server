package api

import (
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/auth"
	"golang.org/x/crypto/bcrypt"
)

// These tests count work, not seconds, so the shard's cost is checked the same
// way on a loaded laptop and an idle runner. scripts/check_api_shard_work.py runs
// them.

func TestShardWork_FixturesHashAtTheMinimumCost(t *testing.T) {
	for name, hash := range map[string]func() (string, error){
		"password":      func() (string, error) { return auth.HashPassword("password") },
		"recovery code": func() (string, error) { return auth.HashRecoveryCode("abcd-efgh") },
	} {
		stored, err := hash()
		if err != nil {
			t.Fatal(err)
		}
		cost, err := bcrypt.Cost([]byte(strings.TrimPrefix(stored, "{BLF-CRYPT}")))
		if err != nil || cost != bcrypt.MinCost {
			t.Errorf("%s hash cost = %d, %v; want %d", name, cost, err, bcrypt.MinCost)
		}
	}
}

// Each described generator validates every example pair, and about 70 tests ask
// for them: built per test, they were an eighth of the shard's CPU.
func TestShardWork_TheGapGeneratorsAreBuiltOnce(t *testing.T) {
	first, second := gapGenerators(t), gapGenerators(t)
	if len(first) != 2 || len(second) != 2 {
		t.Fatalf("gapGenerators returned %d and %d, want 2 each", len(first), len(second))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Errorf("gapGenerators built generator %d again", i)
		}
	}
}
