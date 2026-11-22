package main

import (
	"context"
	"errors"
	"sync"

	"github.com/rest-mail/rest-mail-server/desktop/internal/apiproxy"
	"github.com/wailsapp/wails/v3/pkg/application"
)

type MailStreams struct {
	manager apiproxy.Manager
	mu      sync.Mutex
	active  map[string]context.CancelFunc
}

func (service *MailStreams) Start(accountID int64, subscription string) error {
	service.mu.Lock()
	defer service.mu.Unlock()
	if accountID <= 0 || len(subscription) == 0 || len(subscription) > 64 || len(service.active) >= 32 || !service.manager.Status().SignedIn {
		return errors.New("invalid mail stream subscription")
	}
	if _, exists := service.active[subscription]; exists {
		return errors.New("mail stream already subscribed")
	}
	if service.active == nil {
		service.active = make(map[string]context.CancelFunc)
	}
	ctx, cancel := context.WithCancel(context.Background())
	service.active[subscription] = cancel
	go apiproxy.MailEvents(ctx, service.manager, accountID, func(event apiproxy.MailEvent) {
		application.Get().Event.Emit("mail:stream", map[string]any{"subscription": subscription, "event": event})
	})
	return nil
}

func (service *MailStreams) Stop(subscription string) {
	service.mu.Lock()
	defer service.mu.Unlock()
	if cancel, exists := service.active[subscription]; exists {
		cancel()
		delete(service.active, subscription)
	}
}

func (service *MailStreams) Close() {
	service.mu.Lock()
	defer service.mu.Unlock()
	for subscription, cancel := range service.active {
		cancel()
		delete(service.active, subscription)
	}
}
