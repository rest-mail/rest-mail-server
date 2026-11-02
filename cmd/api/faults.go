//go:build faultinject

package main

import (
	"net/http"

	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/faultinject"
)

func configureFaults(cfg *config.Config) {
	faultinject.Configure(cfg)
}

// injectFaults breaks the public handler with the faults compiled into this
// build, for the contract fault run (#654, tests/contract/faults.yml). Only a
// build with the faultinject tag has this file.
func injectFaults(h http.Handler) http.Handler {
	return faultinject.Wrap(h)
}
