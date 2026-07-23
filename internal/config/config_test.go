package config

import (
	"os"
	"strconv"
	"testing"
	"time"
)

// allEnvKeys lists every environment variable the config package reads.
// We unset them all at the start of each test so that the host environment
// does not leak into the test.
var allEnvKeys = []string{
	"DB_HOST", "DB_PORT", "DB_NAME", "DB_USER", "DB_PASS",
	"DB_MAX_OPEN_CONNS", "DB_MAX_IDLE_CONNS", "DB_CONN_MAX_LIFETIME",
	"API_PORT", "API_HOST", "LOG_LEVEL",
	"TLS_CERT_PATH", "TLS_KEY_PATH",
	"JWT_SECRET", "JWT_ACCESS_EXPIRY", "JWT_REFRESH_EXPIRY",
	"MASTER_KEY",
	"DNS_PROVIDER",
	"GATEWAY_HOSTNAME", "API_BASE_URL",
	"SMTP_PORT_INBOUND", "SMTP_PORT_SUBMISSION", "SMTP_PORT_SUBMISSION_TLS",
	"SMTP_MAX_MESSAGE_SIZE",
	"IMAP_PORT", "IMAP_TLS_PORT",
	"POP3_PORT", "POP3_TLS_PORT",
	"QUEUE_WORKERS", "QUEUE_POLL_INTERVAL",
	"ENVIRONMENT",
}

// clearEnv ensures every config-related env var is truly unset for the test.
// We call t.Setenv first (which snapshots the original value and registers a
// cleanup to restore it), then immediately os.Unsetenv so that os.LookupEnv
// returns ok=false during the test.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, key := range allEnvKeys {
		t.Setenv(key, "")   // register cleanup to restore original value
		_ = os.Unsetenv(key)     // truly unset so LookupEnv sees it as absent
	}
}

func TestLoad_Defaults(t *testing.T) {
	clearEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}

	// Database defaults
	if cfg.DBHost != "localhost" {
		t.Errorf("DBHost = %q, want %q", cfg.DBHost, "localhost")
	}
	if cfg.DBPort != 5432 {
		t.Errorf("DBPort = %d, want %d", cfg.DBPort, 5432)
	}
	if cfg.DBName != "restmail" {
		t.Errorf("DBName = %q, want %q", cfg.DBName, "restmail")
	}
	if cfg.DBUser != "restmail" {
		t.Errorf("DBUser = %q, want %q", cfg.DBUser, "restmail")
	}
	if cfg.DBPass != "restmail" {
		t.Errorf("DBPass = %q, want %q", cfg.DBPass, "restmail")
	}
	if cfg.DBMaxOpenConns != 25 {
		t.Errorf("DBMaxOpenConns = %d, want %d", cfg.DBMaxOpenConns, 25)
	}
	if cfg.DBMaxIdleConns != 10 {
		t.Errorf("DBMaxIdleConns = %d, want %d", cfg.DBMaxIdleConns, 10)
	}
	if cfg.DBConnMaxLifetime != 5*time.Minute {
		t.Errorf("DBConnMaxLifetime = %v, want %v", cfg.DBConnMaxLifetime, 5*time.Minute)
	}

	// API defaults
	if cfg.APIPort != 8080 {
		t.Errorf("APIPort = %d, want %d", cfg.APIPort, 8080)
	}
	if cfg.APIHost != "0.0.0.0" {
		t.Errorf("APIHost = %q, want %q", cfg.APIHost, "0.0.0.0")
	}
	if cfg.LogLevel != "info" {
		t.Errorf("LogLevel = %q, want %q", cfg.LogLevel, "info")
	}

	// TLS defaults (empty)
	if cfg.TLSCertPath != "" {
		t.Errorf("TLSCertPath = %q, want %q", cfg.TLSCertPath, "")
	}
	if cfg.TLSKeyPath != "" {
		t.Errorf("TLSKeyPath = %q, want %q", cfg.TLSKeyPath, "")
	}

	// JWT defaults
	if cfg.JWTSecret != "dev-secret-change-in-production" {
		t.Errorf("JWTSecret = %q, want %q", cfg.JWTSecret, "dev-secret-change-in-production")
	}
	if cfg.JWTAccessExpiry != 15*time.Minute {
		t.Errorf("JWTAccessExpiry = %v, want %v", cfg.JWTAccessExpiry, 15*time.Minute)
	}
	if cfg.JWTRefreshExpiry != 7*24*time.Hour {
		t.Errorf("JWTRefreshExpiry = %v, want %v", cfg.JWTRefreshExpiry, 7*24*time.Hour)
	}

	// MasterKey default (empty)
	if cfg.MasterKey != "" {
		t.Errorf("MasterKey = %q, want %q", cfg.MasterKey, "")
	}

	// DNS
	if cfg.DNSProvider != "dnsmasq" {
		t.Errorf("DNSProvider = %q, want %q", cfg.DNSProvider, "dnsmasq")
	}

	// Gateway defaults
	if cfg.GatewayHostname != "localhost" {
		t.Errorf("GatewayHostname = %q, want %q", cfg.GatewayHostname, "localhost")
	}
	if cfg.APIBaseURL != "http://localhost:8080" {
		t.Errorf("APIBaseURL = %q, want %q", cfg.APIBaseURL, "http://localhost:8080")
	}
	if cfg.SMTPPortInbound != 25 {
		t.Errorf("SMTPPortInbound = %d, want %d", cfg.SMTPPortInbound, 25)
	}
	if cfg.SMTPPortSubmission != 587 {
		t.Errorf("SMTPPortSubmission = %d, want %d", cfg.SMTPPortSubmission, 587)
	}
	if cfg.SMTPPortSubmissionTLS != 465 {
		t.Errorf("SMTPPortSubmissionTLS = %d, want %d", cfg.SMTPPortSubmissionTLS, 465)
	}
	if cfg.IMAPPort != 143 {
		t.Errorf("IMAPPort = %d, want %d", cfg.IMAPPort, 143)
	}
	if cfg.IMAPTLSPort != 993 {
		t.Errorf("IMAPTLSPort = %d, want %d", cfg.IMAPTLSPort, 993)
	}
	if cfg.POP3Port != 110 {
		t.Errorf("POP3Port = %d, want %d", cfg.POP3Port, 110)
	}
	if cfg.POP3TLSPort != 995 {
		t.Errorf("POP3TLSPort = %d, want %d", cfg.POP3TLSPort, 995)
	}
	if cfg.QueueWorkers != 4 {
		t.Errorf("QueueWorkers = %d, want %d", cfg.QueueWorkers, 4)
	}
	if cfg.QueuePollInterval != 5*time.Second {
		t.Errorf("QueuePollInterval = %v, want %v", cfg.QueuePollInterval, 5*time.Second)
	}
	if cfg.SMTPMaxMessageSize != DefaultSMTPMaxMessageSize {
		t.Errorf("SMTPMaxMessageSize = %d, want default %d", cfg.SMTPMaxMessageSize, int64(DefaultSMTPMaxMessageSize))
	}

	// Environment
	if cfg.Environment != "development" {
		t.Errorf("Environment = %q, want %q", cfg.Environment, "development")
	}
}

func TestLoad_OverrideEnvVars(t *testing.T) {
	clearEnv(t)

	t.Setenv("DB_HOST", "dbserver.example.com")
	t.Setenv("API_PORT", "9090")
	t.Setenv("LOG_LEVEL", "debug")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}

	if cfg.DBHost != "dbserver.example.com" {
		t.Errorf("DBHost = %q, want %q", cfg.DBHost, "dbserver.example.com")
	}
	if cfg.APIPort != 9090 {
		t.Errorf("APIPort = %d, want %d", cfg.APIPort, 9090)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel = %q, want %q", cfg.LogLevel, "debug")
	}

	// Verify untouched defaults are still correct.
	if cfg.DBPort != 5432 {
		t.Errorf("DBPort = %d, want default %d", cfg.DBPort, 5432)
	}
	if cfg.DBName != "restmail" {
		t.Errorf("DBName = %q, want default %q", cfg.DBName, "restmail")
	}
}

func TestLoad_ProductionNoSecret(t *testing.T) {
	clearEnv(t)

	t.Setenv("ENVIRONMENT", "production")
	// JWT_SECRET is left at its default value.

	_, err := Load()
	if err == nil {
		t.Fatal("Load() should return an error when ENVIRONMENT=production and JWT_SECRET is the default")
	}
}

func TestLoad_ProductionNoMasterKey(t *testing.T) {
	clearEnv(t)

	t.Setenv("ENVIRONMENT", "production")
	t.Setenv("JWT_SECRET", "my-strong-production-secret")
	// MASTER_KEY is not set.

	_, err := Load()
	if err == nil {
		t.Fatal("Load() should return an error when ENVIRONMENT=production and MASTER_KEY is empty")
	}
}

func TestLoad_ProductionWithSecret(t *testing.T) {
	clearEnv(t)

	t.Setenv("ENVIRONMENT", "production")
	t.Setenv("JWT_SECRET", "my-strong-production-secret")
	t.Setenv("MASTER_KEY", "my-strong-master-key")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}

	if cfg.JWTSecret != "my-strong-production-secret" {
		t.Errorf("JWTSecret = %q, want %q", cfg.JWTSecret, "my-strong-production-secret")
	}
	if cfg.Environment != "production" {
		t.Errorf("Environment = %q, want %q", cfg.Environment, "production")
	}
	if cfg.MasterKey != "my-strong-master-key" {
		t.Errorf("MasterKey = %q, want %q", cfg.MasterKey, "my-strong-master-key")
	}
}

func TestDSN(t *testing.T) {
	cfg := &Config{
		DBHost: "dbhost",
		DBPort: 5433,
		DBUser: "myuser",
		DBPass: "mypass",
		DBName: "mydb",
	}

	want := "host=dbhost port=5433 user=myuser password=mypass dbname=mydb sslmode=disable"
	got := cfg.DSN()

	if got != want {
		t.Errorf("DSN() = %q, want %q", got, want)
	}
}

func TestAPIAddr(t *testing.T) {
	cfg := &Config{
		APIHost: "0.0.0.0",
		APIPort: 8080,
	}

	want := "0.0.0.0:8080"
	got := cfg.APIAddr()

	if got != want {
		t.Errorf("APIAddr() = %q, want %q", got, want)
	}

	// Also verify with a custom host/port.
	cfg.APIHost = "127.0.0.1"
	cfg.APIPort = 3000

	want = "127.0.0.1:3000"
	got = cfg.APIAddr()

	if got != want {
		t.Errorf("APIAddr() = %q, want %q", got, want)
	}
}

func TestLoad_IntParsing(t *testing.T) {
	clearEnv(t)

	// Set DB_PORT to a non-numeric value; getEnvInt should fall back to the default.
	t.Setenv("DB_PORT", "not-a-number")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}

	if cfg.DBPort != 5432 {
		t.Errorf("DBPort = %d, want fallback %d after invalid int", cfg.DBPort, 5432)
	}
}

// ── SMTP max message size ──────────────────────────────────────────────

func TestLoad_SMTPMaxMessageSize_Default(t *testing.T) {
	clearEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}
	if cfg.SMTPMaxMessageSize != 10*1024*1024 {
		t.Errorf("SMTPMaxMessageSize = %d, want 10 MiB default", cfg.SMTPMaxMessageSize)
	}
	// Empty string behaves like unset (env files commonly render KEY=).
	t.Setenv("SMTP_MAX_MESSAGE_SIZE", "")
	cfg, err = Load()
	if err != nil {
		t.Fatalf("Load() with empty SMTP_MAX_MESSAGE_SIZE errored: %v", err)
	}
	if cfg.SMTPMaxMessageSize != 10*1024*1024 {
		t.Errorf("SMTPMaxMessageSize with empty env = %d, want 10 MiB default", cfg.SMTPMaxMessageSize)
	}
}

func TestLoad_SMTPMaxMessageSize_AdminValueRespected(t *testing.T) {
	clearEnv(t)

	// Any positive value is respected — 128 MB, 1 GB, the admin's choice.
	for _, want := range []int64{1024, 128 * 1000 * 1000, 1 << 30} {
		t.Setenv("SMTP_MAX_MESSAGE_SIZE", strconv.FormatInt(want, 10))
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() with SMTP_MAX_MESSAGE_SIZE=%d errored: %v", want, err)
		}
		if cfg.SMTPMaxMessageSize != want {
			t.Errorf("SMTPMaxMessageSize = %d, want %d", cfg.SMTPMaxMessageSize, want)
		}
	}
}

func TestLoad_SMTPMaxMessageSize_Invalid(t *testing.T) {
	clearEnv(t)

	// Zero/negative → startup error (a max must always exist), and unlike the
	// lenient getEnvInt pattern a malformed value is also an error rather than
	// a silent fallback.
	for _, bad := range []string{"0", "-1", "10MB", "banana"} {
		t.Setenv("SMTP_MAX_MESSAGE_SIZE", bad)
		if _, err := Load(); err == nil {
			t.Errorf("Load() with SMTP_MAX_MESSAGE_SIZE=%q should fail", bad)
		}
	}
}

func TestSMTPMaxMessageSizeWarning(t *testing.T) {
	// At or below the 100 MiB threshold: no warning.
	for _, quiet := range []int64{1024, DefaultSMTPMaxMessageSize, SMTPMaxMessageSizeWarnThreshold} {
		cfg := &Config{SMTPMaxMessageSize: quiet}
		if w := cfg.SMTPMaxMessageSizeWarning(); w != "" {
			t.Errorf("SMTPMaxMessageSizeWarning() for %d = %q, want empty", quiet, w)
		}
	}
	// Above the threshold: warn (but Load still accepts the value — verified
	// in TestLoad_SMTPMaxMessageSize_AdminValueRespected with 1 GiB).
	cfg := &Config{SMTPMaxMessageSize: SMTPMaxMessageSizeWarnThreshold + 1}
	if w := cfg.SMTPMaxMessageSizeWarning(); w == "" {
		t.Error("SMTPMaxMessageSizeWarning() above threshold should be non-empty")
	}
}

func TestLoad_DurationParsing(t *testing.T) {
	clearEnv(t)

	t.Setenv("JWT_ACCESS_EXPIRY", "30m")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}

	if cfg.JWTAccessExpiry != 30*time.Minute {
		t.Errorf("JWTAccessExpiry = %v, want %v", cfg.JWTAccessExpiry, 30*time.Minute)
	}

	// Verify that an invalid duration falls back to the default.
	clearEnv(t)
	t.Setenv("JWT_ACCESS_EXPIRY", "invalid-duration")

	cfg, err = Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}

	if cfg.JWTAccessExpiry != 15*time.Minute {
		t.Errorf("JWTAccessExpiry = %v, want fallback %v after invalid duration", cfg.JWTAccessExpiry, 15*time.Minute)
	}
}
