package api

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/push"
	"github.com/rest-mail/rest-mail-server/internal/quiethours"
)

type personalPushRecorder struct {
	sent []push.Notification
}

func (recorder *personalPushRecorder) Send(_ context.Context, notification push.Notification) (push.Result, error) {
	recorder.sent = append(recorder.sent, notification)
	return push.Result{Status: http.StatusOK}, nil
}

func TestAC03QuietHoursAPIGatesContentFreePushAndRevokedSessions(t *testing.T) {
	fixture := newCredentialFixture(t)
	reader := personalMailboxKey(t, fixture.db, fixture.accountID, fixture.address, models.CapMailRead)
	writer := personalMailboxKey(t, fixture.db, fixture.accountID, fixture.address, models.CapMailWrite)
	path := fmt.Sprintf("/api/v1/accounts/%d/quiet-hours", fixture.accountID)
	setting := quiethours.Setting{Enabled: true, Start: "22:00", End: "08:00", TimeZone: "UTC"}
	const enabled = `{"enabled":true,"start":"22:00","end":"08:00","time_zone":"UTC"}`
	checkSetting := func(want quiethours.Setting) {
		t.Helper()
		response := fixture.do(http.MethodGet, path, reader, "")
		var result struct{ Data quiethours.Setting }
		decodeInto(t, response, &result)
		var stored models.Mailbox
		if err := fixture.db.First(&stored, fixture.mailboxID).Error; err != nil {
			t.Fatal(err)
		}
		actual := quiethours.Setting{Enabled: stored.QuietHoursEnabled, Start: stored.QuietHoursStart, End: stored.QuietHoursEnd, TimeZone: stored.QuietHoursTimeZone}
		if response.Code != http.StatusOK || result.Data != want || actual != want {
			t.Fatalf("quiet hours: API=%+v stored=%+v want=%+v; %d %s", result.Data, actual, want, response.Code, response.Body)
		}
	}
	checkSetting(quiethours.Setting{})
	if response := fixture.do(http.MethodPut, path, reader, enabled); response.Code != http.StatusForbidden {
		t.Fatalf("read-only preference write: %d %s", response.Code, response.Body)
	}
	checkSetting(quiethours.Setting{})
	if response := fixture.do(http.MethodPut, path, writer, enabled); response.Code != http.StatusOK {
		t.Fatalf("save quiet hours: %d %s", response.Code, response.Body)
	}
	checkSetting(setting)
	invalid := `{"enabled":true,"start":"22:00","end":"08:00","time_zone":"not/a/zone"}`
	if response := fixture.do(http.MethodPut, path, writer, invalid); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid preference must be refused: %d %s", response.Code, response.Body)
	}
	var mailbox models.Mailbox
	if err := fixture.db.First(&mailbox, fixture.mailboxID).Error; err != nil {
		t.Fatal(err)
	}
	foreign := fixture.seedMailbox(mailbox.DomainID, "foreign-notifications")
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		response := fixture.do(method, fmt.Sprintf("/api/v1/accounts/%d/quiet-hours", foreign), writer, enabled)
		if response.Code != http.StatusNotFound {
			t.Fatalf("foreign preference %s: %d %s", method, response.Code, response.Body)
		}
	}
	checkSetting(setting)
	now := time.Date(2020, time.January, 2, 23, 0, 0, 0, time.UTC)
	active := map[string]models.PushDevice{}
	var revoked []string
	for _, platform := range []string{models.PushPlatformAPNs, models.PushPlatformWebPush} {
		for _, state := range []string{"active", "revoked"} {
			session := "personal-" + platform + "-" + state
			if err := fixture.db.Create(&models.RefreshToken{Jti: session, SessionID: session, AccountID: fixture.accountID, Status: models.RefreshTokenActive, ExpiresAt: now.Add(48 * time.Hour)}).Error; err != nil {
				t.Fatal(err)
			}
			device := models.PushDevice{SessionID: session, MailboxID: fixture.mailboxID, Platform: platform, Token: session,
				Environment: models.APNsProduction, WebPushP256dh: "p256dh-" + session, WebPushAuth: "auth-" + session}
			if err := fixture.db.Create(&device).Error; err != nil {
				t.Fatal(err)
			}
			if state == "active" {
				active[platform] = device
			} else {
				revoked = append(revoked, session)
			}
		}
	}
	var messages []models.Message
	for index := range 2 {
		message := models.Message{MailboxID: fixture.mailboxID, Folder: "INBOX", Sender: "private-sender@remote.test", Subject: fmt.Sprintf("Private subject %d", index),
			BodyText: "Private body never carried in a push", CreatedAt: now.Add(time.Duration(index) * time.Minute), ReceivedAt: now}
		if err := fixture.db.Create(&message).Error; err != nil {
			t.Fatal(err)
		}
		messages = append(messages, message)
	}
	var held []models.QuietNotification
	if err := fixture.db.Where("mailbox_id = ?", fixture.mailboxID).Order("message_id").Find(&held).Error; err != nil || len(held) != 2 || held[0].MessageID != messages[0].ID || held[1].MessageID != messages[1].ID {
		t.Fatalf("API preference did not persist both arrival receipts: %+v (%v)", held, err)
	}
	recorder := &personalPushRecorder{}
	store := push.NewStore(fixture.db, models.PushPlatformAPNs, models.PushPlatformWebPush)
	dispatcher := push.NewDispatcher(store, recorder, time.Second)
	dispatcher.NewMail(fixture.mailboxID)
	dispatcher.Step(t.Context(), now.Add(2*time.Minute))
	if len(recorder.sent) != 0 {
		t.Fatalf("persisted API quiet hours leaked %d pushes", len(recorder.sent))
	}
	messagePath := fmt.Sprintf("/api/v1/messages/%d", messages[0].ID)
	response := fixture.do(http.MethodGet, messagePath, reader, "")
	if response.Code != http.StatusOK {
		t.Fatalf("quiet mail is not readable: %d %s", response.Code, response.Body)
	}
	if response := fixture.do(http.MethodPatch, messagePath, writer, `{"is_read":true}`); response.Code != http.StatusOK {
		t.Fatalf("mark quiet mail read: %d %s", response.Code, response.Body)
	}
	if err := fixture.db.Model(&models.RefreshToken{}).Where("session_id IN ?", revoked).Update("status", models.RefreshTokenRevoked).Error; err != nil {
		t.Fatal(err)
	}
	if response := fixture.do(http.MethodPut, path, writer, `{"enabled":false}`); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("omitting required preference fields must remain invalid: %d %s", response.Code, response.Body)
	}
	checkSetting(setting)
	if response := fixture.do(http.MethodPut, path, writer, `{"enabled":false,"start":"","end":"","time_zone":""}`); response.Code != http.StatusOK {
		t.Fatalf("disable future quiet hours: %d %s", response.Code, response.Body)
	}
	checkSetting(quiethours.Setting{})
	dispatcher.Close()
	restarted := push.NewDispatcher(store, recorder, time.Second)
	defer restarted.Close()
	end := time.Date(2020, time.January, 3, 8, 0, 0, 0, time.UTC)
	restarted.Step(t.Context(), end.Add(-time.Nanosecond))
	if len(recorder.sent) != 0 {
		t.Fatalf("restart or preference change released %d pushes before the saved quiet window ended", len(recorder.sent))
	}
	restarted.Step(t.Context(), end)
	if len(recorder.sent) != len(active) {
		t.Fatalf("summary pushes=%d, want one for each live APNs/Web Push device (%d)", len(recorder.sent), len(active))
	}
	seen := map[string]bool{}
	for _, notification := range recorder.sent {
		device, exists := active[notification.Platform]
		want := fmt.Sprintf(`{"aps":{"content-available":1},"restmail":{"v":1,"device":"%d","account":%d,"unread":1,"arrived":2}}`, device.ID, fixture.mailboxID)
		if !exists || seen[notification.Platform] || notification.Token != device.Token || string(notification.Payload) != want {
			t.Fatalf("summary leaked mail content, used stale counts, duplicated a device or targeted a revoked session: %+v payload=%s want=%s", notification, notification.Payload, want)
		}
		if notification.Platform == models.PushPlatformWebPush && (notification.P256dh != device.WebPushP256dh || notification.Auth != device.WebPushAuth) {
			t.Fatalf("browser push did not retain its subscription encryption keys: %+v", notification)
		}
		seen[notification.Platform] = true
	}
	if err := fixture.db.Where("mailbox_id = ?", fixture.mailboxID).Find(&held).Error; err != nil || len(held) != 0 {
		t.Fatalf("summary left arrival receipts to replay: %+v (%v)", held, err)
	}
	restarted.Step(t.Context(), end.Add(time.Minute))
	if len(recorder.sent) != len(active) {
		t.Fatal("a completed summary was sent again")
	}
	for index, original := range messages {
		var stored models.Message
		if err := fixture.db.First(&stored, original.ID).Error; err != nil {
			t.Fatal(err)
		}
		if stored.Subject != original.Subject || stored.BodyText != original.BodyText || stored.Folder != "INBOX" || stored.IsRead != (index == 0) {
			t.Fatalf("notification changed mail state: %+v", stored)
		}
	}
	var foreignSetting models.Mailbox
	if err := fixture.db.First(&foreignSetting, foreign).Error; err != nil || !reflect.DeepEqual(quiethours.Setting{}, quiethours.Setting{Enabled: foreignSetting.QuietHoursEnabled, Start: foreignSetting.QuietHoursStart, End: foreignSetting.QuietHoursEnd, TimeZone: foreignSetting.QuietHoursTimeZone}) {
		t.Fatalf("foreign preference changed: %+v (%v)", foreignSetting, err)
	}
}
