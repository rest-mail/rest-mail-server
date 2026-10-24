package handlers

import (
	"net/http"

	apitypes "github.com/restmail/restmail/internal/api/types"
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
func describeServer(*http.Request, struct{}) (apitypes.ServerDescription, error) {
	return apitypes.ServerDescription{Product: "rest-mail", Version: version.Version}, nil
}
