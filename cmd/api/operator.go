package main

import (
	"bufio"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"strings"
	"time"

	dkim "github.com/rest-mail/go-dkim"
	"github.com/restmail/restmail/internal/bootstrap"
	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/db"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/dns"
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

      Output: {"purpose": ..., "token": ..., "expires_at": ...}

  restmail-api reset-password --username NAME < password
      Gives an admin the password read from stdin and puts the account back in
      setup: it must choose its own at the next sign-in and enroll a new
      authenticator. Every session it had ends.
      Output: {"username": ..., "setup_required": ["password", "two_factor"]}

  restmail-api revoke-sessions
      Signs everybody out: every refresh token, then every admin's epoch.
      Output: {"refresh_tokens": N, "admins": N}

  restmail-api dns-records --mail-host HOST [--ip ADDRESS]
      The records each domain must publish: MX, SPF, DMARC, and DKIM where the
      domain has a key. ADDRESS adds the mail host's A record and the PTR.
      Output: {"domains": [{"domain": ..., "records": [...]}]}

Results are one JSON object on stdout. Errors: {"error": {"code": ...,
"message": ...}} on stderr, with exit status 2 (usage), 3 (already_claimed),
4 (no_such_admin) or 1 (anything else). Nothing else is written to either;
RESTMAIL_OPERATOR_DEBUG=1 adds server logs to stderr.
Usually run for you: chore password:reset, chore sessions:revoke,
chore dns:show.
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
	// The database library's default logger writes to the process's stdout, and
	// connecting uses it before any session logger of ours applies — a database
	// still starting up then prints a connection error ahead of the result. It
	// gets ours from the start.
	gormlogger.Default = operatorDBLogger(stderr)

	switch args[0] {
	case "mint-token":
		return mintToken(args[1:], stdout, stderr)
	case "reset-password":
		return resetPassword(args[1:], os.Stdin, stdout, stderr)
	case "revoke-sessions":
		return revokeSessions(args[1:], stdout, stderr)
	case "dns-records":
		return dnsRecords(args[1:], stdout, stderr)
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

	database, _, status := openDatabase(stderr)
	if status != exitOK {
		return status
	}

	var token string
	var err error
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

// openDatabase connects with the API's own configuration and brings the schema
// up to date. On failure it has already written the error and returns its exit
// status.
func openDatabase(stderr io.Writer) (*gorm.DB, *config.Config, int) {
	cfg, err := config.Load()
	if err != nil {
		return nil, nil, fail(stderr, exitFailed, "config", err.Error())
	}
	database, err := db.WaitForDB(cfg, 30*time.Second)
	if err != nil {
		return nil, nil, fail(stderr, exitFailed, "database", err.Error())
	}
	// The database library logs SQL straight to the process's stdout, through a
	// writer of its own that slog does not govern, and in development it logs
	// every statement. Here stdout is the result, so it gets a logger of ours:
	// silent, or on stderr with RESTMAIL_OPERATOR_DEBUG.
	database = database.Session(&gorm.Session{Logger: operatorDBLogger(stderr)})
	// The same additive steps the server runs at boot, so this works against a
	// database the server has not started on yet.
	if err := db.AutoMigrate(database, false); err != nil {
		return nil, nil, fail(stderr, exitFailed, "database", err.Error())
	}
	if err := bootstrap.EnsureRBAC(database); err != nil {
		return nil, nil, fail(stderr, exitFailed, "database", err.Error())
	}
	return database, cfg, exitOK
}

// resetPassword reads the new password from stdin rather than a flag, so it
// is never another process's command line.
func resetPassword(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("reset-password", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	username := fs.String("username", "", "the admin whose password to reset")
	if err := fs.Parse(args); err != nil {
		return fail(stderr, exitUsage, "bad_flag", err.Error())
	}
	if *username == "" {
		return fail(stderr, exitUsage, "bad_flag", "--username is required")
	}
	line, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return fail(stderr, exitUsage, "no_password", err.Error())
	}
	password := strings.TrimRight(line, "\r\n")
	if len(password) < bootstrap.MinAdminPasswordLength {
		return fail(stderr, exitUsage, "password_too_short", bootstrap.ErrResetPasswordTooShort.Error()+" (read from stdin)")
	}

	database, _, status := openDatabase(stderr)
	if status != exitOK {
		return status
	}
	admin, err := bootstrap.ResetPassword(database, *username, password)
	switch {
	case errors.Is(err, bootstrap.ErrNoSuchAdmin):
		return fail(stderr, exitNoSuchAdmin, "no_such_admin", fmt.Sprintf("no admin named %q", *username))
	case err != nil:
		return fail(stderr, exitFailed, "reset_failed", err.Error())
	}
	_ = json.NewEncoder(stdout).Encode(map[string]any{
		"username":       admin.Username,
		"setup_required": []string{"password", "two_factor"},
	})
	return exitOK
}

func revokeSessions(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		return fail(stderr, exitUsage, "bad_flag", "revoke-sessions takes no arguments")
	}
	database, _, status := openDatabase(stderr)
	if status != exitOK {
		return status
	}
	revoked, err := bootstrap.RevokeAllSessions(database)
	if err != nil {
		return fail(stderr, exitFailed, "revoke_failed", err.Error())
	}
	_ = json.NewEncoder(stdout).Encode(revoked)
	return exitOK
}

// dnsRecords lists what every domain must publish. The DKIM record is derived
// from the domain's private key, which is why this runs where MASTER_KEY is.
func dnsRecords(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("dns-records", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	mailHost := fs.String("mail-host", "", "the name the server answers mail on")
	ip := fs.String("ip", "", "its public address, if known")
	if err := fs.Parse(args); err != nil {
		return fail(stderr, exitUsage, "bad_flag", err.Error())
	}
	if *mailHost == "" {
		return fail(stderr, exitUsage, "bad_flag", "--mail-host is required")
	}
	database, cfg, status := openDatabase(stderr)
	if status != exitOK {
		return status
	}
	var domains []models.Domain
	if err := database.Order("name").Find(&domains).Error; err != nil {
		return fail(stderr, exitFailed, "database", err.Error())
	}
	type domainRecords struct {
		Domain  string          `json:"domain"`
		Records []dns.DNSRecord `json:"records"`
		// Problem says why a record is missing, when one is: no DKIM key yet,
		// or one this server cannot read.
		Problem string `json:"problem,omitempty"`
	}
	out := struct {
		Domains []domainRecords `json:"domains"`
	}{Domains: []domainRecords{}}
	for _, d := range domains {
		entry := domainRecords{Domain: d.Name}
		var dkimValue string
		switch {
		case d.DKIMPrivateKey == "" || d.DKIMSelector == "":
			entry.Problem = "no DKIM key: mail from this domain goes out unsigned"
		default:
			value, err := dkimRecordValue(d.DKIMPrivateKey, cfg.MasterKey)
			if err != nil {
				entry.Problem = "DKIM key unreadable: " + err.Error()
			}
			dkimValue = value
		}
		entry.Records = dns.MailRecords(d.Name, *mailHost, *ip, d.DKIMSelector, dkimValue)
		out.Domains = append(out.Domains, entry)
	}
	_ = json.NewEncoder(stdout).Encode(out)
	return exitOK
}

func dkimRecordValue(stored, masterKey string) (string, error) {
	keyPEM, err := models.LoadDKIMPrivateKey(stored, masterKey)
	if err != nil {
		return "", err
	}
	priv, err := dkim.ParsePrivateKey(keyPEM)
	if err != nil {
		return "", err
	}
	pubDER, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		return "", err
	}
	return dkim.RecordValue(string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})))
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
