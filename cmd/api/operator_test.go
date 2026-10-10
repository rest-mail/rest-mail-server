package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/bootstrap"
	"github.com/restmail/restmail/internal/config"
	rmdb "github.com/restmail/restmail/internal/db"
	"gorm.io/gorm"
)

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// scratchDatabase points the operator command (which reads its configuration
// from the environment, as in the container) at a database of its own, dropped
// afterwards, and returns a connection to it for the test's own checks.
func scratchDatabase(t *testing.T) *gorm.DB {
	t.Helper()
	port, _ := strconv.Atoi(envOr("DB_PORT", "5432"))
	cfg := config.Config{
		DBHost: envOr("DB_HOST", "localhost"), DBPort: port,
		DBName: envOr("DB_NAME", "restmail"), DBUser: envOr("DB_USER", "restmail"), DBPass: envOr("DB_PASS", "restmail"),
	}
	admin, err := rmdb.Connect(&cfg)
	if err != nil {
		t.Skipf("operator test skipped: no database reachable (%v)", err)
	}
	name := fmt.Sprintf("restmail_operator_%d_%d", os.Getpid(), time.Now().UnixNano())
	if err := admin.Exec("CREATE DATABASE " + name).Error; err != nil {
		t.Skipf("operator test skipped: cannot create a database (%v)", err)
	}
	cfg.DBName = name
	scratch, err := rmdb.Connect(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if s, err := scratch.DB(); err == nil {
			_ = s.Close()
		}
		admin.Exec("DROP DATABASE IF EXISTS " + name)
		if s, err := admin.DB(); err == nil {
			_ = s.Close()
		}
	})
	t.Setenv("DB_HOST", cfg.DBHost)
	t.Setenv("DB_PORT", strconv.Itoa(cfg.DBPort))
	t.Setenv("DB_NAME", name)
	t.Setenv("DB_USER", cfg.DBUser)
	t.Setenv("DB_PASS", cfg.DBPass)
	t.Setenv("DB_SSLMODE", "disable")
	return scratch
}

// run runs an operator command as `restmail-api <args>` would.
func run(args ...string) (status int, stdout, stderr string) {
	var out, errOut bytes.Buffer
	status = runOperator(args, &out, &errOut)
	return status, out.String(), errOut.String()
}

func errorCodeIn(t *testing.T, stderr string) string {
	t.Helper()
	var e struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(stderr), &e); err != nil {
		t.Fatalf("stderr is not one JSON error object: %q", stderr)
	}
	return e.Error.Code
}

func TestMintToken_ClaimsThenRecovers(t *testing.T) {
	gdb := scratchDatabase(t)

	// A fresh database: the command migrates it and mints a setup token.
	status, out, errOut := run("mint-token", "--purpose", "setup")
	if status != exitOK {
		t.Fatalf("mint setup: status %d, stderr %s", status, errOut)
	}
	var raw map[string]string
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		t.Fatalf("stdout is not one JSON object: %q", out)
	}
	if raw["purpose"] != "setup" || raw["token"] == "" {
		t.Fatalf("output = %v", raw)
	}
	if _, err := time.Parse(time.RFC3339, raw["expires_at"]); err != nil {
		t.Errorf("expires_at %q is not RFC 3339", raw["expires_at"])
	}

	// The token it printed claims the server.
	if _, err := bootstrap.ClaimServer(gdb, raw["token"], "owner", "a passphrase only I know", true); err != nil {
		t.Fatalf("claim with the minted token: %v", err)
	}

	// Claimed: no second setup token, and a distinct status for a script to act on.
	status, _, errOut = run("mint-token", "--purpose", "setup")
	if status != exitAlreadyClaimed || errorCodeIn(t, errOut) != "already_claimed" {
		t.Errorf("second setup mint: status %d, stderr %s; want %d already_claimed", status, errOut, exitAlreadyClaimed)
	}

	status, _, errOut = run("mint-token", "--purpose", "reset-admin", "--username", "nobody")
	if status != exitNoSuchAdmin || errorCodeIn(t, errOut) != "no_such_admin" {
		t.Errorf("reset for nobody: status %d, stderr %s", status, errOut)
	}
	status, out, errOut = run("mint-token", "--purpose", "reset-admin", "--username", "owner", "--lifetime", "5m")
	if status != exitOK {
		t.Fatalf("reset mint: status %d, stderr %s", status, errOut)
	}
	if err := json.Unmarshal([]byte(out), &raw); err != nil || raw["purpose"] != "reset-admin" {
		t.Fatalf("reset output = %q", out)
	}
	if _, err := bootstrap.ResetAdmin(gdb, raw["token"], "another passphrase entirely"); err != nil {
		t.Errorf("reset with the minted token: %v", err)
	}
}

// Only the result reaches stdout, so `$(restmail-api mint-token …)` is the
// token's JSON and nothing else, whatever the server code logs.
func TestMintToken_StdoutIsOnlyTheResult(t *testing.T) {
	scratchDatabase(t)
	status, out, _ := run("mint-token", "--purpose", "setup")
	if status != exitOK {
		t.Fatalf("status %d", status)
	}
	if bytes.Count([]byte(out), []byte("\n")) != 1 {
		t.Errorf("stdout has more than the one result line: %q", out)
	}
}

func TestOperator_UsageErrorsAreJSONWithStatus2(t *testing.T) {
	for _, args := range [][]string{
		{"no-such-command"},
		{"mint-token"},
		{"mint-token", "--purpose", "everything"},
		{"mint-token", "--purpose", "reset-admin"},
		{"mint-token", "--purpose", "setup", "--lifetime", "48h"},
		{"mint-token", "--unknown-flag"},
	} {
		status, out, errOut := run(args...)
		if status != exitUsage {
			t.Errorf("%v: status %d, want %d", args, status, exitUsage)
		}
		if out != "" {
			t.Errorf("%v: printed a result on a usage error: %q", args, out)
		}
		if code := errorCodeIn(t, errOut); code == "" {
			t.Errorf("%v: no error code", args)
		}
	}
}

// stderr is the error object alone, whatever the configuration warns about on
// the way, so a program can read it whole.
func TestOperator_StderrIsOnlyTheError(t *testing.T) {
	scratchDatabase(t)
	t.Setenv("JWT_SECRET", "") // the development default: config.Load warns about it
	if status, _, _ := run("mint-token", "--purpose", "setup"); status != exitOK {
		t.Fatal("first mint failed")
	}
	_, _, errOut := run("mint-token", "--purpose", "reset-admin", "--username", "nobody")
	if bytes.Count([]byte(errOut), []byte("\n")) != 1 {
		t.Errorf("stderr has more than the one error line: %q", errOut)
	}
	errorCodeIn(t, errOut)
}

// Nothing reaches the process's real stdout but the result: the database
// library logs SQL there on its own, bypassing slog, through a writer it took
// when it loaded — so swapping os.Stdout in-process proves nothing. This runs
// the command as its own process, the way kubectl exec does, and reads what
// actually came out.
func TestMintToken_NothingElseOnTheRealStdout(t *testing.T) {
	if os.Getenv("RESTMAIL_OPERATOR_SUBPROCESS") == "1" {
		os.Exit(runOperator(os.Args[len(os.Args)-3:], os.Stdout, os.Stderr))
	}
	scratchDatabase(t)

	cmd := exec.Command(os.Args[0], "-test.run=^TestMintToken_NothingElseOnTheRealStdout$",
		"--", "mint-token", "--purpose", "setup")
	cmd.Env = append(os.Environ(), "RESTMAIL_OPERATOR_SUBPROCESS=1", "ENVIRONMENT=development")
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		t.Fatalf("operator subprocess: %v, stderr %s", err, errOut.String())
	}
	var result map[string]string
	if err := json.Unmarshal(out.Bytes(), &result); err != nil || result["token"] == "" {
		t.Errorf("stdout is not just the result (%d bytes), starting %q",
			out.Len(), out.String()[:min(out.Len(), 160)])
	}
}
