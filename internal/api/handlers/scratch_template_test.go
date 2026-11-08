package handlers

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/bootstrap"
	rmdb "github.com/rest-mail/rest-mail-server/internal/db"
	"github.com/rest-mail/rest-mail-server/internal/testdb"
	"gorm.io/gorm"
)

// scratchStep is set-up beyond a model's table that a scratch database starts with.
type scratchStep struct {
	name string
	run  func(*gorm.DB) error
}

var (
	withFullSchema     = scratchStep{"schema", rmdb.AutoMigrate}
	withRoles          = scratchStep{"roles", bootstrap.EnsureRBAC}
	withAudit          = scratchStep{"audit-log", rmdb.MigrateAuditLog}
	withFolderUIDMarks = scratchStep{"folder-uid-marks", rmdb.MigrateFolderUIDMarks}
	withForeignKeys    = scratchStep{"foreign-keys", func(database *gorm.DB) error {
		database.DisableForeignKeyConstraintWhenMigrating = false
		return nil
	}}
)

// runScratchSetup applies models and steps in the order given, gathering neighbouring
// models into one AutoMigrate, as the tests that list them did.
func runScratchSetup(db *gorm.DB, setup []any) error {
	var models []any
	flush := func() error {
		if len(models) == 0 {
			return nil
		}
		err := db.AutoMigrate(models...)
		models = nil
		return err
	}
	for _, item := range setup {
		step, ok := item.(scratchStep)
		if !ok {
			models = append(models, item)
			continue
		}
		if err := flush(); err != nil {
			return err
		}
		if err := step.run(db); err != nil {
			return fmt.Errorf("%s: %w", step.name, err)
		}
	}
	return flush()
}

func scratchSetupKey(setup []any) string {
	parts := make([]string, 0, len(setup))
	for _, item := range setup {
		if step, ok := item.(scratchStep); ok {
			parts = append(parts, step.name)
			continue
		}
		parts = append(parts, reflect.TypeOf(item).String())
	}
	return strings.Join(parts, ",")
}

func openScratchDB(t *testing.T, prefix string, setup []any) *gorm.DB {
	t.Helper()
	cfg := testDBConfig()
	cfg.DBName = "postgres"
	database := testdb.Clone(t, cfg, prefix, testdb.Setup{
		Name: scratchSetupKey(setup),
		Run:  func(database *gorm.DB) error { return runScratchSetup(database, setup) },
	})
	for _, item := range setup {
		if step, ok := item.(scratchStep); ok && step.name == withForeignKeys.name {
			database.DisableForeignKeyConstraintWhenMigrating = false
		}
	}
	return database
}
