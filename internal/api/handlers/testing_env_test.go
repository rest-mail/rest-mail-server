package handlers

import (
	"testing"

	"github.com/restmail/restmail/internal/config"
)

// The test endpoints must fail closed for any production-like ENVIRONMENT,
// not only the exact string "production" — a mistyped/rebranded value must
// never leave reset/seed/snapshot enabled in production.
func TestProductionLockedFailsClosed(t *testing.T) {
	locked := []string{"production", "prod", "PROD", "Production", "prod-eu", "production-us", "  Prod  "}
	for _, env := range locked {
		h := &TestHandler{cfg: &config.Config{Environment: env}}
		if !h.productionLocked() {
			t.Errorf("ENVIRONMENT=%q should lock test endpoints", env)
		}
	}

	open := []string{"development", "dev", "test", "staging", "local", ""}
	for _, env := range open {
		h := &TestHandler{cfg: &config.Config{Environment: env}}
		if h.productionLocked() {
			t.Errorf("ENVIRONMENT=%q should NOT lock test endpoints", env)
		}
	}
}
