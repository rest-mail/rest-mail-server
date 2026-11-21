package main

import (
	"context"
	"log"

	"github.com/rest-mail/rest-mail-server/desktop/internal/session"
	"github.com/wailsapp/wails/v3/pkg/application"
)

type SignInResult = session.SignInResult
type SessionStatus = session.SessionStatus

type Session struct {
	manager *session.Manager
}

func NewSession(manager *session.Manager) *Session {
	return &Session{manager: manager}
}

func (service *Session) SignIn(server, email, password string) (SignInResult, error) {
	return service.manager.SignIn(server, email, password)
}

func (service *Session) SubmitTOTP(code string) error {
	return service.manager.SubmitTOTP(code)
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
