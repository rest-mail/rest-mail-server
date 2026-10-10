package api

import (
	"github.com/restmail/restmail/internal/api/middleware"
	"gorm.io/gorm"
)

// The routes here are tested on a database that fails every query, to test the
// capability gates with nothing else in the way. The stale-session check needs
// that database, so it stands aside: it is tested against a real one in the
// handlers package (auth_claim_test.go).
func init() {
	adminSessionCheck = func(*gorm.DB) middleware.AdminSessionCurrent {
		return func(uint, int) (bool, error) { return true, nil }
	}
}
