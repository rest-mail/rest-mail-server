package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"time"

	"github.com/restmail/restmail/internal/bootstrap"
	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/db"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// Operator commands run inside the API's own container, with its configuration
// and database, through `kubectl exec` or `docker exec` — the restmail CLI does
// that for you. They are reachable from no network: being able to run one is
// being able to control the deployment, which is what proves ownership.
//
// Written for programs as much as people: the result is one JSON object on
// stdout, an error is one JSON object on stderr, and the exit status says which
// kind of failure it was.

// Exit statuses an operator command can return.
const (
	exitOK             = 0
	exitFailed         = 1 // anything unexpected: no database, a bug
	exitUsage          = 2 // a command or flag that does not exist, or is missing
	exitAlreadyClaimed = 3 // setup asked for, but the server has an admin
	exitNoSuchAdmin    = 4 // reset-admin named an account that does not exist
)

const operatorUsage = `restmail-api operator commands

  restmail-api mint-token --purpose setup [--lifetime 15m]
      A single-use token that claims this server while it has no admin.

  restmail-api mint-token --purpose reset-admin --username NAME [--lifetime 15m]
      A single-use token that lets an admin choose a new password and enroll a
      new authenticator, for when every other way in is lost.

Output: {"purpose": ..., "token": ..., "expires_at": ...} on stdout.
Errors: {"error": {"code": ..., "message": ...}} on stderr, with exit status
2 (usage), 3 (already_claimed), 4 (no_such_admin) or 1 (anything else).
Nothing else is written to either; RESTMAIL_OPERATOR_DEBUG=1 adds server logs
to stderr.
Usually run through the restmail CLI (restmail setup, restmail reset-admin).
`

// runOperator runs the operator command in args and returns its exit status.
func runOperator(args []string, stdout, stderr io.Writer) int {
	// stdout carries only the result and stderr only the error object, so a
	// program can read each whole. What the server code logs on the way (config
	// warnings, say) goes nowhere unless RESTMAIL_OPERATOR_DEBUG is set.
	logTo := io.Discard
	if os.Getenv("RESTMAIL_OPERATOR_DEBUG") != "" {
		logTo = stderr
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(logTo, &slog.HandlerOptions{Level: slog.LevelDebug})))

	switch args[0] {
	case "mint-token":
		return mintToken(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		_, _ = io.WriteString(stdout, operatorUsage)
		return exitOK
	default:
		return fail(stderr, exitUsage, "unknown_command", fmt.Sprintf("unknown command %q; see restmail-api help", args[0]))
	}
}

func mintToken(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("mint-token", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	purpose := fs.String("purpose", "", "setup or reset-admin")
	username := fs.String("username", "", "the admin to reset (reset-admin)")
	lifetime := fs.Duration("lifetime", bootstrap.DefaultTokenLifetime, "how long the token can be redeemed")
	if err := fs.Parse(args); err != nil {
		return fail(stderr, exitUsage, "bad_flag", err.Error())
	}
	if *purpose != "setup" && *purpose != "reset-admin" {
		return fail(stderr, exitUsage, "bad_flag", `--purpose must be "setup" or "reset-admin"`)
	}
	if *purpose == "reset-admin" && *username == "" {
		return fail(stderr, exitUsage, "bad_flag", "--username is required with --purpose reset-admin")
	}
	if *lifetime <= 0 || *lifetime > 24*time.Hour {
		return fail(stderr, exitUsage, "bad_flag", "--lifetime must be between 1s and 24h")
	}

	cfg, err := config.Load()
	if err != nil {
		return fail(stderr, exitFailed, "config", err.Error())
	}
	database, err := db.WaitForDB(cfg, 30*time.Second)
	if err != nil {
		return fail(stderr, exitFailed, "database", err.Error())
	}
	// The database library logs SQL straight to the process's stdout, through a
	// writer of its own that slog does not govern, and in development it logs
	// every statement. Here stdout is the result, so it gets a logger of ours:
	// silent, or on stderr with RESTMAIL_OPERATOR_DEBUG.
	database = database.Session(&gorm.Session{Logger: operatorDBLogger(stderr)})
	// The same additive steps the server runs at boot, so this works against a
	// database the server has not started on yet.
	if err := db.AutoMigrate(database, false); err != nil {
		return fail(stderr, exitFailed, "database", err.Error())
	}
	if err := bootstrap.EnsureRBAC(database); err != nil {
		return fail(stderr, exitFailed, "database", err.Error())
	}

	var token string
	var expires time.Time
	if *purpose == "setup" {
		token, expires, err = bootstrap.MintSetupToken(database, *lifetime)
	} else {
		token, expires, err = bootstrap.MintResetToken(database, *username, *lifetime)
	}
	switch {
	case errors.Is(err, bootstrap.ErrAlreadyClaimed):
		return fail(stderr, exitAlreadyClaimed, "already_claimed", err.Error())
	case errors.Is(err, bootstrap.ErrNoSuchAdmin):
		return fail(stderr, exitNoSuchAdmin, "no_such_admin", fmt.Sprintf("no admin named %q", *username))
	case err != nil:
		return fail(stderr, exitFailed, "mint_failed", err.Error())
	}
	_ = json.NewEncoder(stdout).Encode(map[string]string{
		"purpose":    *purpose,
		"token":      token,
		"expires_at": expires.UTC().Format(time.RFC3339),
	})
	return exitOK
}

func fail(stderr io.Writer, status int, code, message string) int {
	_ = json.NewEncoder(stderr).Encode(map[string]any{
		"error": map[string]string{"code": code, "message": message},
	})
	return status
}

func operatorDBLogger(stderr io.Writer) gormlogger.Interface {
	if os.Getenv("RESTMAIL_OPERATOR_DEBUG") == "" {
		return gormlogger.Discard
	}
	return gormlogger.New(log.New(stderr, "", log.LstdFlags), gormlogger.Config{LogLevel: gormlogger.Info})
}
