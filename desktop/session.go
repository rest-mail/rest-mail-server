package main

import (
	"context"
	"log"

	"github.com/rest-mail/rest-mail-server/desktop/internal/session"
	"github.com/wailsapp/wails/v3/pkg/application"
)

type SignInResult = session.SignInResult
type SessionStatus = session.SessionStatus
type TwoFactorEnrollment = session.TwoFactorEnrollment

type SessionManager interface {
	SignIn(string, string, string) (SignInResult, error)
	SubmitTOTP(string) error
	ChangePassword(string, string) (SignInResult, error)
	EnrollTwoFactor() (TwoFactorEnrollment, error)
	ConfirmTwoFactor(string) error
	SignOut() error
	Status() SessionStatus
	SetOnChange(func(session.SessionStatus))
	Restore(context.Context) error
	Run(context.Context)
}

type Session struct {
	manager SessionManager
}

func NewSession(manager SessionManager) *Session {
	return &Session{manager: manager}
}

func (service *Session) SignIn(server, email, password string) (SignInResult, error) {
	return service.manager.SignIn(server, email, password)
}

func (service *Session) SubmitTOTP(code string) error {
	return service.manager.SubmitTOTP(code)
}

// ChangePassword replaces the password during first-run setup or later. The
// server ends the session, so the app signs in again with the new password.
func (service *Session) ChangePassword(current, replacement string) (SignInResult, error) {
	return service.manager.ChangePassword(current, replacement)
}

// EnrollTwoFactor starts adding an authenticator and returns what the user
// must see: the secret, the otpauth URL for a QR code, and recovery codes.
func (service *Session) EnrollTwoFactor() (TwoFactorEnrollment, error) {
	return service.manager.EnrollTwoFactor()
}

// ConfirmTwoFactor switches the new authenticator on with its first code.
func (service *Session) ConfirmTwoFactor(code string) error {
	return service.manager.ConfirmTwoFactor(code)
}

func (service *Session) SignOut() error {
	return service.manager.SignOut()
}

func (service *Session) Status() SessionStatus {
	return service.manager.Status()
}

func (service *Session) ServiceStartup(ctx context.Context, _ application.ServiceOptions) error {
	service.manager.SetOnChange(func(status session.SessionStatus) {
		application.Get().Event.Emit("session:changed", status)
	})
	if err := service.manager.Restore(ctx); err != nil {
		log.Printf("Could not restore desktop session: %v", err)
	}
	go service.manager.Run(ctx)
	return nil
}
