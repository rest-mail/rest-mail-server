package handlers

import "github.com/rest-mail/rest-mail-server/internal/auth"

// hashPassword and hashRecoveryCode hash what the handlers store: they are
// auth's own, at its real bcrypt cost. They are variables only so this
// package's tests can hash at bcrypt.MinCost (see TestMain); nothing outside
// the tests assigns them.
var (
	hashPassword     = auth.HashPassword
	hashRecoveryCode = auth.HashRecoveryCode
)
