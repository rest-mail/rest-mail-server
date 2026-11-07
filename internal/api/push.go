package api

import (
	"log/slog"

	"github.com/rest-mail/rest-mail-server/internal/api/handlers"
	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/push"
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
	if w := cfg.WebPush(); w.Partial() {
		slog.Warn("web push is off: set both RESTMAIL_WEBPUSH_VAPID_KEY_FILE and RESTMAIL_WEBPUSH_VAPID_SUBJECT to turn it on")
	} else if w.Configured() {
		if _, err := push.NewWebPush(webPushConfig(w)); err != nil {
			return err
		}
	}
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

// webPushConfig is the Web Push sender's configuration, from the server's.
func webPushConfig(s config.WebPushSettings) push.WebPushConfig {
	return push.WebPushConfig{KeyFile: s.KeyFile, Subject: s.Subject}
}

// pushService is push notifications (#363), when APNs is configured: the
// dispatcher that sends, the store of devices, and the devices routes.
type pushService struct {
	dispatcher *push.Dispatcher
	store      *push.Store
	devices    *handlers.DeviceHandler
}

// startPushDispatcher starts the background worker. This package's tests replace
// it: a router test's database is a fake answering that test alone, and the
// worker's own queries would race it.
var startPushDispatcher = func(dispatcher *push.Dispatcher) { dispatcher.Start() }

// newPushService starts push when APNs or Web Push is configured, and is nil
// otherwise: then the devices routes do not exist, and answer 404.
func newPushService(db *gorm.DB, cfg *config.Config) *pushService {
	senders := push.Senders{}
	var topic string
	var web *push.WebPush
	if s := cfg.APNs(); s.Configured() {
		sender, err := push.NewAPNs(apnsConfig(s))
		if err != nil {
			slog.Error("APNs push is off: the APNs key could not be loaded", "error", err)
		} else {
			senders[models.PushPlatformAPNs] = sender
			topic = s.Topic
			slog.Info("push notifications are on", "service", "apns", "topic", s.Topic)
		}
	}
	if s := cfg.WebPush(); s.Configured() {
		sender, err := push.NewWebPush(webPushConfig(s))
		if err != nil {
			slog.Error("web push is off: the VAPID key could not be loaded", "error", err)
		} else {
			senders[models.PushPlatformWebPush] = sender
			web = sender
			slog.Info("push notifications are on", "service", "webpush")
		}
	}
	if len(senders) == 0 {
		return nil
	}
	platforms := make([]string, 0, len(senders))
	for _, p := range []string{models.PushPlatformAPNs, models.PushPlatformWebPush} {
		if _, ok := senders[p]; ok {
			platforms = append(platforms, p)
		}
	}
	store := push.NewStore(db, platforms...)
	dispatcher := push.NewDispatcher(store, senders, push.DefaultCoalesceWindow)
	startPushDispatcher(dispatcher)
	devices := handlers.NewDeviceHandler(db, topic)
	if web != nil {
		devices.SetWebPush(web.PublicKey())
	}
	return &pushService{dispatcher: dispatcher, store: store, devices: devices}
}

// close stops the dispatcher. Safe on nil.
func (p *pushService) close() {
	if p != nil {
		p.dispatcher.Close()
	}
}
