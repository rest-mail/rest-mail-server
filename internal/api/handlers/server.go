package handlers

import (
	"net/http"

	"github.com/restmail/restmail/internal/version"
)

// describeServer answers GET /api/v1/server: what this server is, for anyone
// who can reach it.
//
// A client cannot look behind the server — it does not know, and must not
// need to know, how the server was installed — so the server describes itself.
// The version is the one deliberate disclosure, so that what is running can be
// confirmed from outside. Nothing else is said: there is nothing to choose
// between before signing in, because the first admin comes from the password
// the installer chose.
func describeServer(*http.Request, struct{}) (serverDescription, error) {
	return serverDescription{Product: "rest-mail", Version: version.Version}, nil
}

// serverDescription is the body of GET /api/v1/server.
type serverDescription struct {
	Product string `json:"product" enums:"rest-mail"`
	Version string `json:"version" description:"e.g. 0.7.0; dev for a build with none stamped in"`
}
