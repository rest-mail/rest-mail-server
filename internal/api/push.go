package api

import (
	"log/slog"

	"github.com/restmail/restmail/internal/api/handlers"
	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/push"
	"gorm.io/gorm"
)

// apnsConfig is the APNs sender's configuration, from the server's.
func apnsConfig(s config.APNsSettings) push.APNsConfig {
	return push.APNsConfig{KeyFile: s.KeyFile, KeyID: s.KeyID, TeamID: s.TeamID, Topic: s.Topic}
}

// CheckPush checks the push configuration before the server starts. Push is
// off unless APNs is fully configured, and a partial configuration only
// warns; but a key that is set and will not load would turn push off without
// a word, so it is an error.
func CheckPush(cfg *config.Config) error {
	s := cfg.APNs()
	if s.Partial() {
		slog.Warn("push notifications are off: set all of RESTMAIL_APNS_KEY_FILE, RESTMAIL_APNS_KEY_ID and RESTMAIL_APNS_TEAM_ID to turn them on")
		return nil
	}
	if !s.Configured() {
		return nil
	}
	_, err := push.NewAPNs(apnsConfig(s))
	return err
}

// pushService is push notifications (#363), when APNs is configured: the
// dispatcher that sends, the store of devices, and the devices routes.
type pushService struct {
	dispatcher *push.Dispatcher
	store      *push.Store
	devices    *handlers.DeviceHandler
}

// newPushService starts push when APNs is configured, and is nil otherwise:
// then the devices routes do not exist, and answer 404.
func newPushService(db *gorm.DB, cfg *config.Config) *pushService {
	s := cfg.APNs()
	if !s.Configured() {
		return nil
	}
	sender, err := push.NewAPNs(apnsConfig(s))
	if err != nil {
		slog.Error("push notifications are off: the APNs key could not be loaded", "error", err)
		return nil
	}
	store := push.NewStore(db)
	dispatcher := push.NewDispatcher(store, sender, push.DefaultCoalesceWindow)
	dispatcher.Start()
	slog.Info("push notifications are on", "service", "apns", "topic", s.Topic)
	return &pushService{dispatcher: dispatcher, store: store, devices: handlers.NewDeviceHandler(db, s.Topic)}
}

// close stops the dispatcher. Safe on nil.
func (p *pushService) close() {
	if p != nil {
		p.dispatcher.Close()
	}
}
