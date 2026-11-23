package config

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestSMTPGatewayMTASTSPolicy(test *testing.T) {
	for _, environment := range []string{"", "production", "development", "test"} {
		for _, setting := range []string{"", "true", "false", "FALSE", "0", "no"} {
			test.Run("environment="+environment+"/enforce="+setting, func(test *testing.T) {
				clearEnv(test)
				validSecretsProd(test)
				if environment != "" {
					test.Setenv("ENVIRONMENT", environment)
				}
				if setting != "" {
					test.Setenv("MTASTS_ENFORCE", setting)
				}
				test.Setenv("DB_SSLMODE", "require")
				cfg, err := Load()
				if err != nil {
					test.Fatal(err)
				}
				enabled := setting == "" || setting == "true"
				if cfg.MTASTSEnforce != enabled {
					test.Fatalf("loaded MTA-STS enforcement = %t, want %t", cfg.MTASTSEnforce, enabled)
				}
				cfg.TLSCertPath, cfg.TLSKeyPath = "/certs/mail.test.crt", "/certs/mail.test.key"
				cfg.SRSSecret = strings.Repeat("f", MinSRSSecretLength)
				var warnings bytes.Buffer
				previous := slog.Default()
				slog.SetDefault(slog.New(slog.NewTextHandler(&warnings, nil)))
				test.Cleanup(func() { slog.SetDefault(previous) })
				err = cfg.ValidateListenerSecurity(RoleSMTPGateway)
				if !enabled && (environment == "" || environment == "production") {
					if err == nil || !strings.Contains(err.Error(), "MTASTS_ENFORCE") {
						test.Fatalf("expected production to refuse disabled MTA-STS, got %v", err)
					}
					return
				}
				if err != nil {
					test.Fatal(err)
				}
				if warned := strings.Contains(warnings.String(), "MTASTS_ENFORCE"); warned != !enabled {
					test.Fatalf("MTA-STS warning = %t, want %t; logs: %s", warned, !enabled, warnings.String())
				}
			})
		}
	}
}
