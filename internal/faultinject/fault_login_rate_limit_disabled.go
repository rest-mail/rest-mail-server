//go:build faultinject && fault_login_rate_limit_disabled

package faultinject

import "github.com/rest-mail/rest-mail-server/internal/config"

func init() {
	register(Fault{
		Name:   "login_rate_limit_disabled",
		Method: "POST",
		Path:   "/api/v1/auth/login",
		Configure: func(cfg *config.Config) {
			cfg.AuthRateLimitEnabled = false
		},
	})
}
