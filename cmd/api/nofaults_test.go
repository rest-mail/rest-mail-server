//go:build !faultinject

package main

import (
	"net/http"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/config"
)

// A normal build serves the router itself, with no fault in front of it.
func TestANormalBuildInjectsNoFault(t *testing.T) {
	cfg := config.Config{APIMaxBodyBytes: 512, AuthRateLimitEnabled: true}
	want := cfg
	configureFaults(&cfg)
	if cfg.APIMaxBodyBytes != want.APIMaxBodyBytes || cfg.AuthRateLimitEnabled != want.AuthRateLimitEnabled {
		t.Fatal("a normal build changed the configured limits")
	}
	router := http.NewServeMux()
	if got := injectFaults(router); got != http.Handler(router) {
		t.Fatal("a build without the faultinject tag wrapped the public handler")
	}
}
