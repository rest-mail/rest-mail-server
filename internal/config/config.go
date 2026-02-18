package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	// Database
	DBHost            string
	DBPort            int
	DBName            string
	DBUser            string
	DBPass            string
	DBMaxOpenConns    int
	DBMaxIdleConns    int
	DBConnMaxLifetime time.Duration

	// API Server
	APIPort  int
	APIHost  string
	LogLevel string

	// TLS
	TLSCertPath string
	TLSKeyPath  string
	TLSCertDir  string // directory containing per-domain cert/key pairs for SNI

	// JWT
	JWTSecret            string
	JWTAccessExpiry      time.Duration
	JWTRefreshExpiry     time.Duration

	// Master key for encrypting private keys at rest
	MasterKey string

	// DNS Provider
	DNSProvider string

	// Gateway
	GatewayHostname string
	APIBaseURL      string // internal API URL for the gateway
	SMTPPortInbound int
	SMTPPortSubmission int
	SMTPPortSubmissionTLS int
	IMAPPort        int
	IMAPTLSPort     int
	POP3Port        int
	POP3TLSPort     int
	QueueWorkers    int
	QueuePollInterval time.Duration

	// CORS
	CORSAllowedOrigins []string

	// Environment
	Environment string // "development", "production", "test"
}

func Load() (*Config, error) {
	cfg := &Config{
		DBHost:            getEnv("DB_HOST", "localhost"),
		DBPort:            getEnvInt("DB_PORT", 5432),
		DBName:            getEnv("DB_NAME", "restmail"),
		DBUser:            getEnv("DB_USER", "restmail"),
		DBPass:            getEnv("DB_PASS", "restmail"),
		DBMaxOpenConns:    getEnvInt("DB_MAX_OPEN_CONNS", 25),
		DBMaxIdleConns:    getEnvInt("DB_MAX_IDLE_CONNS", 10),
		DBConnMaxLifetime: getEnvDuration("DB_CONN_MAX_LIFETIME", 5*time.Minute),

		APIPort:  getEnvInt("API_PORT", 8080),
		APIHost:  getEnv("API_HOST", "0.0.0.0"),
		LogLevel: getEnv("LOG_LEVEL", "info"),

		TLSCertPath: getEnv("TLS_CERT_PATH", ""),
		TLSKeyPath:  getEnv("TLS_KEY_PATH", ""),
		TLSCertDir:  getEnv("TLS_CERT_DIR", ""),

		JWTSecret:        getEnv("JWT_SECRET", "dev-secret-change-in-production"),
		JWTAccessExpiry:  getEnvDuration("JWT_ACCESS_EXPIRY", 15*time.Minute),
		JWTRefreshExpiry: getEnvDuration("JWT_REFRESH_EXPIRY", 7*24*time.Hour),

		MasterKey: getEnv("MASTER_KEY", ""),

		DNSProvider: getEnv("DNS_PROVIDER", "dnsmasq"),

		GatewayHostname:       getEnv("GATEWAY_HOSTNAME", "mail3.test"),
		APIBaseURL:            getEnv("API_BASE_URL", "http://localhost:8080"),
		SMTPPortInbound:       getEnvInt("SMTP_PORT_INBOUND", 25),
		SMTPPortSubmission:    getEnvInt("SMTP_PORT_SUBMISSION", 587),
		SMTPPortSubmissionTLS: getEnvInt("SMTP_PORT_SUBMISSION_TLS", 465),
		IMAPPort:              getEnvInt("IMAP_PORT", 143),
		IMAPTLSPort:           getEnvInt("IMAP_TLS_PORT", 993),
		POP3Port:              getEnvInt("POP3_PORT", 110),
		POP3TLSPort:           getEnvInt("POP3_TLS_PORT", 995),
		QueueWorkers:          getEnvInt("QUEUE_WORKERS", 4),
		QueuePollInterval:     getEnvDuration("QUEUE_POLL_INTERVAL", 5*time.Second),

		CORSAllowedOrigins: getEnvSlice("CORS_ALLOWED_ORIGINS", []string{"http://localhost:3000"}),

		Environment: getEnv("ENVIRONMENT", "development"),
	}

	if cfg.JWTSecret == "dev-secret-change-in-production" && cfg.Environment == "production" {
		return nil, fmt.Errorf("JWT_SECRET must be set in production")
	}

	if cfg.MasterKey == "" && cfg.Environment == "production" {
		return nil, fmt.Errorf("MASTER_KEY must be set in production")
	}

	return cfg, nil
}

func (c *Config) DSN() string {
	return fmt.Sprintf(
		"host=%s port=%d user=%s password=%s dbname=%s sslmode=disable",
		c.DBHost, c.DBPort, c.DBUser, c.DBPass, c.DBName,
	)
}

func (c *Config) APIAddr() string {
	return fmt.Sprintf("%s:%d", c.APIHost, c.APIPort)
}

func getEnv(key, fallback string) string {
	if val, ok := os.LookupEnv(key); ok {
		return val
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if val, ok := os.LookupEnv(key); ok {
		if i, err := strconv.Atoi(val); err == nil {
			return i
		}
	}
	return fallback
}

func getEnvSlice(key string, fallback []string) []string {
	if val, ok := os.LookupEnv(key); ok {
		var result []string
		for _, s := range strings.Split(val, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				result = append(result, s)
			}
		}
		if len(result) > 0 {
			return result
		}
	}
	return fallback
}

func getEnvDuration(key string, fallback time.Duration) time.Duration {
	if val, ok := os.LookupEnv(key); ok {
		if d, err := time.ParseDuration(val); err == nil {
			return d
		}
	}
	return fallback
}
