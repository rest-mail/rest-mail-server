package session

import "errors"

const KeychainService = "REST Mail"

var ErrSignedOut = errors.New("not signed in")

type SignInResult struct {
	NeedsTOTP bool
}

type SessionStatus struct {
	SignedIn bool
	Server   string
	Email    string
}

type APIError struct {
	Status  int
	Code    string
	Message string
}

func (err *APIError) Error() string {
	return err.Message
}
