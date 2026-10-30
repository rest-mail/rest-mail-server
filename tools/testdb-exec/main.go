// Command testdb-exec gives each test binary a PostgreSQL database of its own.
//
// go test runs every package's tests as a separate process, several at once,
// and the database tests all read DB_NAME. Against one shared database they
// migrate the same schema and append to the same audit log side by side, and
// their locks meet: a test fails with "deadlock detected" because of what a
// different package happened to be doing. Run as
//
//	go build -o "$RUNNER_TEMP/testdb-exec" ./tools/testdb-exec
//	go test -exec "$RUNNER_TEMP/testdb-exec" ./internal/...
//
// it creates an empty database for the binary, runs it with DB_NAME naming that
// database, and drops it afterwards. DB_NAME itself is only where it connects
// to create and drop: nothing is written there. DB_HOST, DB_PORT, DB_USER and
// DB_PASS are read as the tests read them.
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"time"

	"github.com/jackc/pgx/v5"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: testdb-exec BINARY [ARGS...] (as go test -exec)")
		os.Exit(2)
	}
	code, err := run(os.Args[1], os.Args[2:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "testdb-exec:", err)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}

// run creates the binary's database, runs it there, and drops the database
// whatever the binary's outcome. It returns the binary's exit code.
func run(binary string, args []string) (int, error) {
	ctx := context.Background()
	maintenance := env("DB_NAME", "restmail")
	name := fmt.Sprintf("%s_%d_%d", maintenance, os.Getpid(), time.Now().UnixNano())

	if err := admin(ctx, maintenance, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		return 1, fmt.Errorf("create the test database: %w", err)
	}

	cmd := exec.Command(binary, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = append(os.Environ(), "DB_NAME="+name)
	runErr := cmd.Run()

	// FORCE ends a connection a test leaked, which would otherwise keep the
	// database from being dropped.
	dropErr := admin(ctx, maintenance, "DROP DATABASE IF EXISTS "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")

	if dropErr != nil && runErr != nil {
		fmt.Fprintf(os.Stderr, "testdb-exec: drop the test database %s: %v\n", name, dropErr)
	}

	var exit *exec.ExitError
	switch {
	case errors.As(runErr, &exit):
		return exit.ExitCode(), nil
	case runErr != nil:
		return 1, runErr
	case dropErr != nil:
		return 1, fmt.Errorf("drop the test database %s: %w", name, dropErr)
	}
	return 0, nil
}

// admin runs one statement against the maintenance database. CREATE and DROP
// DATABASE cannot run inside a transaction, so it is a connection of its own.
func admin(ctx context.Context, database, statement string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	dsn := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(env("DB_USER", "restmail"), env("DB_PASS", "restmail")),
		Host:     net.JoinHostPort(env("DB_HOST", "localhost"), env("DB_PORT", "5432")),
		Path:     "/" + database,
		RawQuery: "sslmode=disable",
	}
	conn, err := pgx.Connect(ctx, dsn.String())
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(ctx) }()
	_, err = conn.Exec(ctx, statement)
	return err
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
