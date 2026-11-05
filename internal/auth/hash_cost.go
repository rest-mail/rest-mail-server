package auth

import (
	"sync/atomic"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

// productionHashCost is the bcrypt cost of every password and recovery code the
// server stores. A binary that is not a test binary always hashes at this cost.
const productionHashCost = 10

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

func hashCost() int {
	if minimumHashCostInTests.Load() {
		return bcrypt.MinCost
	}
	return productionHashCost
}
