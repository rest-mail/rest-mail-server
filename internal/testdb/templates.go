package testdb

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/rest-mail/rest-mail-server/internal/config"
	rmdb "github.com/rest-mail/rest-mail-server/internal/db"
	"gorm.io/gorm"
)

type Setup struct {
	Name string
	Run  func(*gorm.DB) error
}

func Models(models ...any) Setup {
	names := make([]string, len(models))
	for index, model := range models {
		modelType := reflect.TypeOf(model)
		for modelType.Kind() == reflect.Pointer {
			modelType = modelType.Elem()
		}
		names[index] = modelType.PkgPath() + "/" + modelType.String()
	}
	return Setup{
		Name: "models:" + strings.Join(names, ","),
		Run:  func(database *gorm.DB) error { return database.AutoMigrate(models...) },
	}
}

type templateDatabase struct {
	config config.Config
	name   string
	err    error
}

var (
	templatesMu sync.Mutex
	templates   = map[string]*templateDatabase{}
	databaseID  atomic.Uint64
	databaseRun = fmt.Sprintf("%d_%d", os.Getpid(), time.Now().UnixNano())
)

func nextDatabaseName(prefix string) string {
	return fmt.Sprintf("restmail_%s_%s_%d", prefix, databaseRun, databaseID.Add(1))
}

func templateFor(admin *gorm.DB, cfg config.Config, setup []Setup) (string, error) {
	keys := []string{cfg.DBHost, fmt.Sprint(cfg.DBPort), cfg.DBUser, cfg.DBName}
	for _, step := range setup {
		keys = append(keys, step.Name)
	}
	key := strings.Join(keys, "\x00")
	templatesMu.Lock()
	defer templatesMu.Unlock()
	if existing := templates[key]; existing != nil {
		return existing.name, existing.err
	}
	entry := &templateDatabase{config: cfg, name: nextDatabaseName("template")}
	if err := admin.Exec("CREATE DATABASE " + pgx.Identifier{entry.name}.Sanitize()).Error; err != nil {
		return "", err
	}
	templates[key] = entry
	cfg.DBName = entry.name
	database, err := rmdb.Connect(&cfg)
	if err != nil {
		entry.err = err
		return "", err
	}
	pool, err := database.DB()
	if err != nil {
		entry.err = err
		return "", err
	}
	for _, step := range setup {
		if err := step.Run(database); err != nil {
			entry.err = fmt.Errorf("%s: %w", step.Name, err)
			break
		}
	}
	entry.err = errors.Join(entry.err, pool.Close())
	return entry.name, entry.err
}

func Clone(t testing.TB, configuration *config.Config, prefix string, setup ...Setup) *gorm.DB {
	t.Helper()
	cfg := *configuration
	if cfg.DBMaxIdleConns == 0 {
		cfg.DBMaxIdleConns = 2
	}
	admin, err := rmdb.Connect(&cfg)
	if err != nil {
		Unreachable(t, "template database server unavailable: %v", err)
	}
	ReuseConnections(t, admin)
	var current string
	if err := admin.Raw("SELECT current_database()").Scan(&current).Error; err != nil || current != cfg.DBName {
		t.Fatalf("admin database identity = %q, want %q, err = %v", current, cfg.DBName, err)
	}
	template, err := templateFor(admin, cfg, setup)
	if err != nil {
		t.Fatalf("prepare migrated template: %v", err)
	}
	name := nextDatabaseName(prefix)
	if len(name) > 63 {
		t.Fatalf("scratch database name exceeds PostgreSQL identifier length: %q", name)
	}
	if err := admin.Exec("CREATE DATABASE " + pgx.Identifier{name}.Sanitize() + " TEMPLATE " + pgx.Identifier{template}.Sanitize()).Error; err != nil {
		t.Fatalf("clone migrated template: %v", err)
	}
	t.Cleanup(func() {
		if err := admin.Exec("DROP DATABASE " + pgx.Identifier{name}.Sanitize()).Error; err != nil {
			t.Errorf("drop cloned database %s: %v", name, err)
			return
		}
		var remaining int64
		if err := admin.Raw("SELECT count(*) FROM pg_database WHERE datname = ?", name).Scan(&remaining).Error; err != nil || remaining != 0 {
			t.Errorf("cloned database %s remains after drop: count %d, err %v", name, remaining, err)
		}
	})
	cfg.DBName = name
	database, err := rmdb.Connect(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	ReuseConnections(t, database)
	if err := database.Raw("SELECT current_database()").Scan(&current).Error; err != nil || current != name {
		t.Fatalf("clone database identity = %q, want %q, err = %v", current, name, err)
	}
	t.Logf("verified isolated database %s copied from migrated template %s", name, template)
	return database
}

func CloseTemplates() error {
	templatesMu.Lock()
	defer templatesMu.Unlock()
	var failures []error
	for key, entry := range templates {
		admin, err := rmdb.Connect(&entry.config)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		err = admin.Exec("DROP DATABASE IF EXISTS " + pgx.Identifier{entry.name}.Sanitize()).Error
		pool, poolErr := admin.DB()
		if poolErr == nil {
			poolErr = pool.Close()
		}
		if err == nil {
			delete(templates, key)
		}
		if err = errors.Join(err, poolErr); err != nil {
			failures = append(failures, fmt.Errorf("drop template %s: %w", entry.name, err))
		}
	}
	return errors.Join(failures...)
}
