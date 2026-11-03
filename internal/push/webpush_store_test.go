package push

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
)

// TestWebPushDevicesQuietHoursAndGone: a browser's subscription gets the same
// pushes as a phone, carrying its keys to the sender; quiet hours hold it
// back exactly as they do APNs; and a 410 from its push service removes it,
// audited with the push service as the reason.
func TestWebPushDevicesQuietHoursAndGone(t *testing.T) {
	database := quietTestDB(t)
	now := time.Date(2026, 11, 3, 2, 0, 0, 0, time.UTC) // 03:00 in Berlin
	domain := models.Domain{Name: "webpush.example.test", Active: true}
	if err := database.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	seed := func(local string, quiet bool) (models.Mailbox, models.PushDevice) {
		t.Helper()
		account := models.Mailbox{DomainID: domain.ID, LocalPart: local, Address: local + "@" + domain.Name, Active: true,
			QuietHoursEnabled: quiet, QuietHoursStart: "22:00", QuietHoursEnd: "08:00", QuietHoursTimeZone: "Europe/Berlin"}
		if err := database.Create(&account).Error; err != nil {
			t.Fatal(err)
		}
		session := "webpush-" + local
		if err := database.Create(&models.RefreshToken{Jti: session, SessionID: session, AccountID: account.ID,
			Status: models.RefreshTokenActive, ExpiresAt: now.Add(72 * time.Hour)}).Error; err != nil {
			t.Fatal(err)
		}
		device := models.PushDevice{SessionID: session, MailboxID: account.ID, Platform: models.PushPlatformWebPush,
			Token: "https://push.example.com/send/" + local, WebPushP256dh: "p256dh-" + local, WebPushAuth: "auth-" + local}
		if err := database.Create(&device).Error; err != nil {
			t.Fatal(err)
		}
		if err := database.Create(&models.Message{MailboxID: account.ID, Folder: "INBOX", Sender: "private@example.test",
			Subject: "private subject", BodyText: "secret body", CreatedAt: now, ReceivedAt: now}).Error; err != nil {
			t.Fatal(err)
		}
		return account, device
	}
	held, _ := seed("held", true)
	open, device := seed("open", false)

	sender := &fakeSender{sent: make(chan Notification, 8), result: Result{Status: http.StatusGone, Reason: "Unregistered"}}
	store := NewStore(database, models.PushPlatformAPNs, models.PushPlatformWebPush)
	dispatcher := NewDispatcher(store, sender, time.Second)
	dispatcher.NewMail(held.ID)
	dispatcher.NewMail(open.ID)
	dispatcher.Step(context.Background(), now)

	if len(sender.sent) != 1 {
		t.Fatalf("%d pushes: quiet hours must hold the held account's", len(sender.sent))
	}
	n := <-sender.sent
	if n.Platform != models.PushPlatformWebPush || n.Token != device.Token || n.P256dh != "p256dh-open" || n.Auth != "auth-open" {
		t.Fatalf("notification %+v", n)
	}
	if string(n.Payload) != string(Payload(DeviceID(&device), open.ID, 1)) {
		t.Fatalf("payload %s is not the APNs signal", n.Payload)
	}

	var left int64
	if err := database.Model(&models.PushDevice{}).Where("id = ?", device.ID).Count(&left).Error; err != nil || left != 0 {
		t.Fatalf("the gone subscription is still there (%d, %v)", left, err)
	}
	var entry models.AuditEntry
	if err := database.Where("action = ?", ActionDeviceRemove).Order("id desc").First(&entry).Error; err != nil {
		t.Fatal(err)
	}
	if entry.Target["reason"] != RemovedByWebPush || entry.Target["webpush_reason"] != "Unregistered" || entry.Target["platform"] != models.PushPlatformWebPush {
		t.Fatalf("audit entry %v", entry.Target)
	}
}
