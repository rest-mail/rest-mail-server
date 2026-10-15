package main

import (
	"flag"
	"fmt"
	"io"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// `restmail password-hash` turns the first admin's password into what a
// deployment can hold instead of it: a bcrypt hash, which the server checks
// sign-ins against and nobody can sign in with. install:pulumi puts it in a
// stack's config as an ordinary value, so nothing about it needs the stack's
// passphrase.

const passwordHashUsage = `restmail password-hash — a bcrypt hash of an admin password, for a deployment to hold

  printf '%s' "$PASSWORD" | restmail password-hash

Prints the hash, which the server takes as RESTMAIL_BOOTSTRAP_ADMIN_PASSWORD_HASH
(Helm: api.bootstrapAdminPasswordHash) in place of the password. The password is
read from stdin, never from the command line, and must be the server's minimum
of 16 characters: a hash cannot be checked for length once it is made.

Exit status: 0 ok, 2 usage.
`

// The server's own: its minimum bootstrap password, and the cost it hashes
// passwords at (internal/bootstrap, internal/auth).
const (
	minBootstrapPassword = 16
	passwordHashCost     = 10
)

func runPasswordHash(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("password-hash", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	help := fs.Bool("help", false, "show help")
	o := output{stdout: stdout, stderr: stderr}
	if err := fs.Parse(args); err != nil {
		return o.fail(exitUsage, "bad_flag", err.Error())
	}
	if *help {
		_, _ = io.WriteString(stdout, passwordHashUsage)
		return exitOK
	}
	if fs.NArg() > 0 {
		return o.fail(exitUsage, "bad_flag", "the password comes on stdin, never on the command line")
	}
	if isTerminal(stdin) {
		return o.fail(exitUsage, "no_password", `the password comes on stdin: printf '%s' "$PASSWORD" | restmail password-hash`)
	}
	raw, err := io.ReadAll(io.LimitReader(stdin, 4096))
	if err != nil {
		return o.fail(exitFailed, "read_failed", err.Error())
	}
	password := strings.TrimRight(string(raw), "\r\n")
	if len(password) < minBootstrapPassword {
		return o.fail(exitUsage, "too_short", fmt.Sprintf("the password must be at least %d characters", minBootstrapPassword))
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), passwordHashCost)
	if err != nil {
		return o.fail(exitFailed, "hash_failed", err.Error())
	}
	fmt.Fprintln(stdout, string(hash))
	return exitOK
}
