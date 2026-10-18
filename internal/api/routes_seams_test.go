package api

import (
	"sync"

	"github.com/restmail/restmail/internal/api/handlers"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/grants"
)

// The routes here are tested on a database that fails every query, to test the
// capability gates with nothing else in the way. The stale-session check needs
// that database, so it stands aside: it is tested against a real one in
// sessions_db_test.go and the handlers package. So do the grants: each test
// admin holds what testAdminHolding gave it, at "*", any other account holds
// nothing, and the grants themselves are tested against a real database
// (grants_access_test.go).
func init() {
	sessionCheck = func(*handlers.SessionCache) middleware.SessionCurrent {
		return func(uint, int) (bool, error) { return true, nil }
	}
	heldGrants = func(*grants.Cache) middleware.HeldGrants {
		return func(accountID uint) (grants.Held, error) {
			testGrants.Lock()
			defer testGrants.Unlock()
			return testGrants.held[accountID], nil
		}
	}
}

var testGrants = struct {
	sync.Mutex
	next uint
	held map[uint]grants.Held
}{next: 1000, held: map[uint]grants.Held{}}

// testAdminHolding is a new account id holding caps at "*": an admin.
func testAdminHolding(caps []string) uint {
	testGrants.Lock()
	defer testGrants.Unlock()
	testGrants.next++
	entries := make([]grants.Entry, len(caps))
	for i, c := range caps {
		entries[i] = grants.Entry{Scope: grants.All, Capability: c}
	}
	testGrants.held[testGrants.next] = grants.HeldOf(entries...)
	return testGrants.next
}
