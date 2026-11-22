package main

import (
	"context"

	"github.com/rest-mail/rest-mail-server/desktop/internal/localmail"
	"github.com/rest-mail/rest-mail-server/desktop/internal/mailbackend"
)

type LocalMail struct {
	service *localmail.Service
	manager *mailbackend.Manager
}

func (mail *LocalMail) Status(accountID uint) (localmail.IndexStatus, error) {
	return mail.service.IndexStatus(context.Background(), accountID)
}

func (mail *LocalMail) Reindex(accountID uint) error {
	return mail.service.Reindex(context.Background(), accountID)
}

func (mail *LocalMail) SignOut(deleteData bool) error {
	if deleteData {
		if err := mail.service.ForgetAll(); err != nil {
			return err
		}
	}
	return mail.manager.SignOut()
}
