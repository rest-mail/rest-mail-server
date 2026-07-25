package main

import (
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/db"
)

func main() {
	logHandler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})
	slog.SetDefault(slog.New(logHandler))

	slog.Info("running database migrations")

	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	database, err := db.WaitForDB(cfg, 30*time.Second)
	if err != nil {
		slog.Error("failed to connect to database", "error", err)
		os.Exit(1)
	}

	// The migrate tool is the controlled, deploy-time entry point where destructive
	// one-time upgrades (e.g. the linked_accounts dedupe) may run — but only when
	// the operator explicitly opts in with DB_ALLOW_DESTRUCTIVE_MIGRATIONS=true.
	// Without it, this run is additive-only, exactly like a server boot (issue #196).
	allowDestructive := strings.EqualFold(strings.TrimSpace(os.Getenv("DB_ALLOW_DESTRUCTIVE_MIGRATIONS")), "true")
	if allowDestructive {
		slog.Warn("destructive migrations enabled (DB_ALLOW_DESTRUCTIVE_MIGRATIONS=true): one-time data-collapsing steps may run")
	}

	if err := db.AutoMigrate(database, allowDestructive); err != nil {
		slog.Error("migration failed", "error", err)
		os.Exit(1)
	}

	slog.Info("migrations completed successfully")
}
