package session

import (
	"errors"

	"github.com/rest-mail/rest-mail-server/desktop/internal/instance"
)

var KeychainService = instance.KeychainService()

var ErrSignedOut = errors.New("not signed in")

type SignInResult struct {
	NeedsTOTP bool
	// SetupRequired lists the first-run steps the server still asks for, in
	// the server's words: "password", then "two_factor". Mail is refused
	// until it is empty.
	SetupRequired []string
}

type SessionStatus struct {
	SignedIn      bool
	Server        string
	Email         string
	AccountID     uint
	SetupRequired []string
}

// TwoFactorEnrollment is what the user needs to add an authenticator: the
// secret for manual entry, the otpauth URL a QR code encodes, and recovery
// codes the server shows only this once.
type TwoFactorEnrollment struct {
	Secret        string
	OTPAuthURL    string
	RecoveryCodes []string
}

type APIError struct {
	Status  int
	Code    string
	Message string
}

func (err *APIError) Error() string {
	return err.Message
}
