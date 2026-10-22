package db_test

import (
	"fmt"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/config"
	rmdb "github.com/restmail/restmail/internal/db"
)

// Processes sharing a database (replicas, parallel test packages) migrate it at
// once; none may fail on a table another created first.
func TestAutoMigrate_ConcurrentOnFreshDatabase(t *testing.T) {
	env := func(name, fallback string) string {
		if v := os.Getenv(name); v != "" {
			return v
		}
		return fallback
	}
	port, err := strconv.Atoi(env("DB_PORT", "5432"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{DBHost: env("DB_HOST", "localhost"), DBPort: port,
		DBName: env("DB_NAME", "restmail"), DBUser: env("DB_USER", "restmail"), DBPass: env("DB_PASS", "restmail")}
	server, err := rmdb.Connect(&cfg)
	if err != nil {
		t.Skipf("no database reachable: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := server.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	name := fmt.Sprintf("restmail_automigrate_%d_%d", os.Getpid(), time.Now().UnixNano())
	if err := server.Exec("CREATE DATABASE " + name).Error; err != nil {
		t.Fatal(err)
	}
	cfg.DBName = name

	const processes = 4
	var wg sync.WaitGroup
	errs := make(chan error, processes)
	for range processes {
		database, err := rmdb.Connect(&cfg)
		if err != nil {
			t.Fatal(err)
		}
		sqlDB, err := database.DB()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = sqlDB.Close() })
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- rmdb.AutoMigrate(database)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
	// Runs before the pools above close, so FORCE ends their connections.
	t.Cleanup(func() {
		if err := server.Exec("DROP DATABASE IF EXISTS " + name + " WITH (FORCE)").Error; err != nil {
			t.Error(err)
		}
	})
}
