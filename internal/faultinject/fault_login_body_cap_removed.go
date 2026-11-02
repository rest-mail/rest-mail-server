//go:build faultinject && fault_login_body_cap_removed

package faultinject

import "github.com/rest-mail/rest-mail-server/internal/config"

func init() {
	register(Fault{
		Name:   "login_body_cap_removed",
		Method: "POST",
		Path:   "/api/v1/auth/login",
		Configure: func(cfg *config.Config) {
			cfg.APIMaxBodyBytes = 0
		},
	})
}
