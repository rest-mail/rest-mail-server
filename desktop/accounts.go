package main

import (
	"context"
	"log"
	"time"

	"github.com/rest-mail/rest-mail-server/desktop/internal/imapmail"
	"github.com/rest-mail/rest-mail-server/desktop/internal/mailbackend"
	"github.com/rest-mail/rest-mail-server/desktop/internal/session"
)

func newDesktopSession() *mailbackend.Manager {
	accounts, err := imapmail.New()
	if err != nil {
		log.Print("Could not restore IMAP accounts from system keychain")
	}
	return mailbackend.New(session.New(), accounts)
}

type Accounts struct{ manager *mailbackend.Manager }

func (service *Accounts) AddIMAPAccount(input imapmail.AccountInput) (imapmail.Account, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	return service.manager.AddIMAPAccount(ctx, input)
}

func (service *Accounts) RemoveIMAPAccount(id uint) error {
	return service.manager.RemoveIMAPAccount(id)
}
func (service *Accounts) ListAccounts() []imapmail.Account { return service.manager.ListAccounts() }
