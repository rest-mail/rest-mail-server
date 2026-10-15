package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
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

func TestMintToken_LetsAnAdminBackIn(t *testing.T) {
	gdb := scratchDatabase(t)
	bootstrapAdmin(t, gdb)

	// Nothing claims a server any more: its first admin is the bootstrap one.
	status, out, errOut := run("mint-token", "--purpose", "setup")
	if status != exitUsage || out != "" || errorCodeIn(t, errOut) != "bad_flag" {
		t.Errorf("setup mint: status %d, stdout %q, stderr %s; want %d bad_flag", status, out, errOut, exitUsage)
	}

	status, _, errOut = run("mint-token", "--purpose", "reset-admin", "--username", "nobody")
	if status != exitNoSuchAdmin || errorCodeIn(t, errOut) != "no_such_admin" {
		t.Errorf("reset for nobody: status %d, stderr %s", status, errOut)
	}
	status, out, errOut = run("mint-token", "--purpose", "reset-admin", "--username", "admin", "--lifetime", "5m")
	if status != exitOK {
		t.Fatalf("reset mint: status %d, stderr %s", status, errOut)
	}
	var raw map[string]string
	if err := json.Unmarshal([]byte(out), &raw); err != nil || raw["purpose"] != "reset-admin" || raw["token"] == "" {
		t.Fatalf("reset output = %q", out)
	}
	if _, err := time.Parse(time.RFC3339, raw["expires_at"]); err != nil {
		t.Errorf("expires_at %q is not RFC 3339", raw["expires_at"])
	}
	if _, err := bootstrap.ResetAdmin(gdb, raw["token"], "another passphrase entirely"); err != nil {
		t.Errorf("reset with the minted token: %v", err)
	}
}

// Only the result reaches stdout, so `$(restmail-api mint-token …)` is the
// token's JSON and nothing else, whatever the server code logs.
func TestMintToken_StdoutIsOnlyTheResult(t *testing.T) {
	bootstrapAdmin(t, scratchDatabase(t))
	status, out, _ := run("mint-token", "--purpose", "reset-admin", "--username", "admin")
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
		{"mint-token", "--purpose", "setup"},
		{"mint-token", "--purpose", "reset-admin"},
		{"mint-token", "--purpose", "reset-admin", "--username", "admin", "--lifetime", "48h"},
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
	bootstrapAdmin(t, scratchDatabase(t))
	t.Setenv("JWT_SECRET", "") // the development default: config.Load warns about it
	if status, _, _ := run("mint-token", "--purpose", "reset-admin", "--username", "admin"); status != exitOK {
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
		os.Exit(runOperator(os.Args[len(os.Args)-5:], os.Stdout, os.Stderr))
	}
	bootstrapAdmin(t, scratchDatabase(t))

	cmd := exec.Command(os.Args[0], "-test.run=^TestMintToken_NothingElseOnTheRealStdout$",
		"--", "mint-token", "--purpose", "reset-admin", "--username", "admin")
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

// A database that is not answering yet must not put the driver's connection
// errors on stdout ahead of — or instead of — the result: a script reading
// stdout would take them for it. Here it never answers, so stdout stays empty
// and the error is the one JSON object on stderr.
func TestOperator_NothingOnStdoutWhileTheDatabaseIsUnreachable(t *testing.T) {
	if os.Getenv("RESTMAIL_OPERATOR_SUBPROCESS") == "1" {
		os.Exit(runOperator([]string{"revoke-sessions"}, os.Stdout, os.Stderr))
	}
	if testing.Short() {
		t.Skip("waits out the 30s database timeout")
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestOperator_NothingOnStdoutWhileTheDatabaseIsUnreachable$")
	cmd.Env = append(os.Environ(), "RESTMAIL_OPERATOR_SUBPROCESS=1", "ENVIRONMENT=development",
		"DB_HOST=127.0.0.1", "DB_PORT=1", "DB_SSLMODE=disable")
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	_ = cmd.Run()
	if out.Len() != 0 {
		t.Errorf("stdout carried %d bytes, starting %q", out.Len(), out.String()[:min(out.Len(), 160)])
	}
	if code := errorCodeIn(t, lastLine(errOut.String())); code != "database" {
		t.Errorf("error code %q, want database", code)
	}
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}
