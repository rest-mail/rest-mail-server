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
	auth.UseMinimumHashCostInTests()
	hashPassword = minCostHashPassword
	hashRecoveryCode = func(code string) (string, error) {
		return minCostHashPassword(auth.NormalizeRecoveryCode(code))
	}
	code := m.Run()
	dropScratchTemplates()
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
	// testDBSchema is the database and schema connectTestDB migrated.
	testDBSchema string
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
		if testDBErr = bootstrap.EnsureRBAC(testDB); testDBErr != nil {
			return
		}
		testDBSchema, testDBErr = schemaOf(testDB)
	})
	return testDB, testDBErr
}

// schemaOf names the database and schema db is working in.
func schemaOf(db *gorm.DB) (string, error) {
	var schema string
	err := db.Raw("SELECT current_database() || '.' || COALESCE(current_schema(), '')").Scan(&schema).Error
	return schema, err
}

// onSharedSchema says whether db works in the schema connectTestDB migrated: the
// shared pool, a transaction on it, or a pool of a test's own on the same database
// and search path. A scratch database or a per-test schema has its own tables.
func onSharedSchema(db *gorm.DB) bool {
	if testDB == nil {
		return false
	}
	if db.Statement.ConnPool == testDB.ConnPool {
		return true
	}
	schema, err := schemaOf(db)
	return err == nil && schema == testDBSchema
}

// migrateSchema is rmdb.AutoMigrate, which connectTestDB has already run on the
// shared schema. No test takes anything out of that schema, and running it again
// took thousands of catalogue queries (one per column of every table).
func migrateSchema(db *gorm.DB) error {
	if onSharedSchema(db) {
		return nil
	}
	return rmdb.AutoMigrate(db)
}

// migrateModels is db.AutoMigrate for what a test needs. On the shared schema it only
// migrates a model whose table is missing, since connectTestDB migrated every model
// the server has and a test-only one need only be made once.
func migrateModels(db *gorm.DB, models ...any) error {
	if !onSharedSchema(db) {
		return db.AutoMigrate(models...)
	}
	var missing []any
	for _, model := range models {
		if !db.Migrator().HasTable(model) {
			missing = append(missing, model)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return db.AutoMigrate(missing...)
}

// migrateAuditLog is rmdb.MigrateAuditLog, part of what connectTestDB ran. On the
// shared schema it would only lock the table to replace its triggers with the same ones.
func migrateAuditLog(db *gorm.DB) error {
	if onSharedSchema(db) {
		return nil
	}
	return rmdb.MigrateAuditLog(db)
}

// seededRBAC holds what the role tables read just after bootstrap.EnsureRBAC ran on the
// shared schema. Running it again from one of those states changes nothing, since rows
// that exist are left as they are, so it need not run. A test that removed or changed a
// role, capability or link leaves a state not listed here, and it runs for real.
var (
	seededRBACMu sync.Mutex
	seededRBAC   = map[string]bool{}
)

// rbacStateSQL reads every row of the tables EnsureRBAC reads and seeds.
const rbacStateSQL = `SELECT md5(
	COALESCE((SELECT string_agg(c::text, ',' ORDER BY c.id) FROM capabilities c), '') || '|' ||
	COALESCE((SELECT string_agg(r::text, ',' ORDER BY r.id) FROM roles r), '') || '|' ||
	COALESCE((SELECT string_agg(l::text, ',' ORDER BY l.role_id, l.capability_id) FROM role_capabilities l), ''))`

// ensureRBAC is bootstrap.EnsureRBAC, without its few hundred statements where they
// would change nothing.
func ensureRBAC(db *gorm.DB) error {
	if !onSharedSchema(db) {
		return bootstrap.EnsureRBAC(db)
	}
	var state string
	if err := db.Raw(rbacStateSQL).Scan(&state).Error; err != nil {
		return bootstrap.EnsureRBAC(db)
	}
	seededRBACMu.Lock()
	defer seededRBACMu.Unlock()
	if seededRBAC[state] {
		return nil
	}
	if err := bootstrap.EnsureRBAC(db); err != nil {
		return err
	}
	if err := db.Raw(rbacStateSQL).Scan(&state).Error; err != nil {
		return err
	}
	seededRBAC[state] = true
	return nil
}

// accountModels are the tables an account lives in: its mailbox, its
// credentials, and the roles and grants that say what it may do.
var accountModels = []interface{}{
	&models.Domain{}, &models.Mailbox{}, &models.QuotaUsage{}, &models.Credential{},
	&models.ThreadDue{},
	&models.ThreadSnooze{},
	&models.Role{}, &models.Capability{}, &models.RoleCapability{},
	&models.Grant{}, &models.GrantActivation{}, &models.RefreshToken{},
}

func closeTestDB() {
	if testDB == nil {
		return
	}
	if sqlDB, err := testDB.DB(); err == nil {
		_ = sqlDB.Close()
	}
}
