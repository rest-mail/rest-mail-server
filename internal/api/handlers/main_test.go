package handlers

import (
	"os"
	"sync"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/bootstrap"
	"github.com/rest-mail/rest-mail-server/internal/config"
	rmdb "github.com/rest-mail/rest-mail-server/internal/db"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

func TestMain(m *testing.M) {
	// bcrypt is slow on purpose, and many times slower under the race detector
	// CI runs with: at the real cost, the passwords and recovery codes these
	// tests hash and check took the package past go test's timeout. Nothing a
	// test here checks depends on the cost, so they hash at the lowest.
	hashPassword = minCostHashPassword
	hashRecoveryCode = func(code string) (string, error) {
		return minCostHashPassword(auth.NormalizeRecoveryCode(code))
	}
	code := m.Run()
	closeTestDB()
	os.Exit(code)
}

// minCostHashPassword is auth.HashPassword at bcrypt.MinCost: the same
// {BLF-CRYPT} hash auth.CheckPassword reads, made in a fraction of the time.
func minCostHashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		return "", err
	}
	return "{BLF-CRYPT}" + string(hash), nil
}

// The database-backed tests share one connection pool, opened by the first
// test that needs it and closed by TestMain. A pool per test was never closed,
// and with no idle connections kept, every query opened and authenticated a
// connection of its own.
var (
	testDBOnce sync.Once
	testDB     *gorm.DB
	testDBErr  error
)

// testDBConfig is the unit-test Postgres (CI sets DB_*; the defaults match a
// local `postgres` container), keeping idle connections as the server does.
func testDBConfig() *config.Config {
	return &config.Config{
		DBHost:         envOr("DB_HOST", "localhost"),
		DBPort:         envIntOr("DB_PORT", 5432),
		DBName:         envOr("DB_NAME", "restmail"),
		DBUser:         envOr("DB_USER", "restmail"),
		DBPass:         envOr("DB_PASS", "restmail"),
		DBMaxIdleConns: 10,
	}
}

// connectTestDB is the shared pool, or why it could not be opened; a test
// skips on the error, as the repo's depless-local / DB-in-CI convention has it.
//
// The pool opens on the schema and roles a server starts with, whichever test
// comes first. Without them on an empty database there is no server ceiling, so
// grants allow nothing, and a test only passed if an earlier one had left these
// behind (#1868).
func connectTestDB() (*gorm.DB, error) {
	testDBOnce.Do(func() {
		testDB, testDBErr = rmdb.Connect(testDBConfig())
		if testDBErr != nil {
			return
		}
		if testDBErr = rmdb.AutoMigrate(testDB); testDBErr != nil {
			return
		}
		testDBErr = bootstrap.EnsureRBAC(testDB)
	})
	return testDB, testDBErr
}

// accountModels are the tables an account lives in: its mailbox, its
// credentials, and the roles and grants that say what it may do.
var accountModels = []interface{}{
	&models.Domain{}, &models.Mailbox{}, &models.QuotaUsage{}, &models.Credential{},
	&models.ThreadDue{},
	&models.ThreadSnooze{},
	&models.Role{}, &models.Capability{}, &models.RoleCapability{},
	&models.Grant{}, &models.GrantActivation{}, &models.RefreshToken{},
	&models.GatewaySession{},
}

func closeTestDB() {
	if testDB == nil {
		return
	}
	if sqlDB, err := testDB.DB(); err == nil {
		_ = sqlDB.Close()
	}
}
