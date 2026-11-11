package auth

import (
	"sync/atomic"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

var minimumHashCostInTests atomic.Bool

// UseMinimumHashCostInTests makes HashPassword and HashRecoveryCode hash at
// bcrypt.MinCost for the rest of this test binary. bcrypt is slow on purpose, and
// many times slower under the race detector, so a package whose fixtures hash
// thousands of times can spend its whole go test timeout on it.
//
// It panics outside a binary built by go test, so no configuration, environment
// or deployment can lower the cost of a stored hash.
func UseMinimumHashCostInTests() {
	if !testing.Testing() {
		panic("auth: the minimum hash cost is only for test binaries")
	}
	minimumHashCostInTests.Store(true)
}

// passwordHashCost is the cost HashPassword uses and PasswordNeedsRehash holds a
// stored hash to: bcrypt.MinCost in a test binary that asked for the minimum,
// otherwise passwordCost (PasswordHashCost, or what LowerPasswordCostForTests set).
func passwordHashCost() int {
	if minimumHashCostInTests.Load() {
		return bcrypt.MinCost
	}
	return passwordCost
}
