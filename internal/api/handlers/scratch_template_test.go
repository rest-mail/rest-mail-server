package handlers

import (
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/bootstrap"
	rmdb "github.com/rest-mail/rest-mail-server/internal/db"
	"gorm.io/gorm"
)

// scratchStep is set-up beyond a model's table that a scratch database starts with.
type scratchStep struct {
	name string
	run  func(*gorm.DB) error
}

var (
	withFullSchema = scratchStep{"schema", rmdb.AutoMigrate}
	withRoles      = scratchStep{"roles", bootstrap.EnsureRBAC}
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

// A scratch database is a copy of a template holding what its test starts with. Migrating
// is the costly part of one, and no two tests with the same set-up need it done twice;
// each test still has a database of its own, and can still change or drop anything in it.
// The template is made by the first test that asks for it, so a test run alone makes its
// own. TestMain drops them.
var (
	scratchTemplatesMu sync.Mutex
	scratchTemplates   = map[string]string{}
)

func scratchTemplate(admin *gorm.DB, setup []any) (string, error) {
	scratchTemplatesMu.Lock()
	defer scratchTemplatesMu.Unlock()
	key := scratchSetupKey(setup)
	if name, ok := scratchTemplates[key]; ok {
		return name, nil
	}
	name := fmt.Sprintf("restmail_scratchtemplate_%d_%d", os.Getpid(), len(scratchTemplates))
	if err := admin.Exec("CREATE DATABASE " + name).Error; err != nil {
		return "", err
	}
	// Recorded before the set-up runs, so that TestMain drops it even if that fails.
	scratchTemplates[key] = name
	cfg := testDBConfig()
	cfg.DBName = name
	db, err := rmdb.Connect(cfg)
	if err != nil {
		delete(scratchTemplates, key)
		return "", err
	}
	sqlDB, err := db.DB()
	if err != nil {
		delete(scratchTemplates, key)
		return "", err
	}
	// Postgres refuses to copy a database anyone is connected to.
	defer func() { _ = sqlDB.Close() }()
	if err := runScratchSetup(db, setup); err != nil {
		delete(scratchTemplates, key)
		_ = sqlDB.Close()
		_ = admin.Exec("DROP DATABASE IF EXISTS " + name).Error
		return "", err
	}
	return name, nil
}

// dropScratchTemplates removes the templates, for TestMain once every test has finished.
func dropScratchTemplates() {
	if len(scratchTemplates) == 0 {
		return
	}
	cfg := testDBConfig()
	cfg.DBName = "postgres"
	admin, err := rmdb.Connect(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "scratch templates left behind: %v\n", err)
		return
	}
	for _, name := range scratchTemplates {
		if err := admin.Exec("DROP DATABASE IF EXISTS " + name).Error; err != nil {
			fmt.Fprintf(os.Stderr, "scratch template %s left behind: %v\n", name, err)
		}
	}
	if sqlDB, err := admin.DB(); err == nil {
		_ = sqlDB.Close()
	}
}

// openScratchDB is a database of the test's own, named after prefix and dropped with the
// test, holding setup. It never touches the shared pool: even migrations and fixtures
// belong only in a fresh scratch database, whose identity is checked before any writes.
func openScratchDB(t *testing.T, prefix string, setup []any) *gorm.DB {
	t.Helper()
	cfg := testDBConfig()
	cfg.DBName = "postgres"
	admin, err := rmdb.Connect(cfg)
	if err != nil {
		t.Skipf("%s DB test: admin database unavailable: %v", prefix, err)
	}
	adminSQL, err := admin.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adminSQL.Close() })
	var current string
	if err := admin.Raw("SELECT current_database()").Scan(&current).Error; err != nil || current != "postgres" {
		t.Fatalf("admin database identity = %q, err = %v", current, err)
	}
	template, err := scratchTemplate(admin, setup)
	if err != nil {
		t.Fatalf("make scratch template: %v", err)
	}
	name := fmt.Sprintf("restmail_%s_%d_%d", prefix, os.Getpid(), time.Now().UnixNano())
	if err := admin.Exec("CREATE DATABASE " + name + " TEMPLATE " + template).Error; err != nil {
		t.Fatalf("create isolated scratch database: %v", err)
	}
	t.Cleanup(func() {
		if err := admin.Exec("DROP DATABASE " + name).Error; err != nil {
			t.Errorf("drop scratch database: %v", err)
		} else {
			t.Logf("dropped isolated scratch database %s", name)
		}
	})
	cfg.DBName = name
	scratch, err := rmdb.Connect(cfg)
	if err != nil {
		t.Fatal(err)
	}
	scratchSQL, err := scratch.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = scratchSQL.Close() })
	if err := scratch.Raw("SELECT current_database()").Scan(&current).Error; err != nil || current != name {
		t.Fatalf("scratch database identity = %q, want %q, err = %v", current, name, err)
	}
	t.Logf("verified isolated scratch database %s before migrations/writes", current)
	return scratch
}
