package push

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/config"
	rmdb "github.com/rest-mail/rest-mail-server/internal/db"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/testdb"
	"gorm.io/gorm"
)

func quietTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	env := func(key, fallback string) string {
		if value := os.Getenv(key); value != "" {
			return value
		}
		return fallback
	}
	port, _ := strconv.Atoi(env("DB_PORT", "5432"))
	cfg := config.Config{DBHost: env("DB_HOST", "localhost"), DBPort: port, DBName: env("DB_NAME", "restmail"), DBUser: env("DB_USER", "restmail"), DBPass: env("DB_PASS", "restmail"), DBMaxOpenConns: 8, DBMaxIdleConns: 2}
	admin, err := rmdb.Connect(&cfg)
	if err != nil {
		testdb.Unreachable(t, "quiet-hours database unavailable: %v", err)
	}
	name := fmt.Sprintf("quiet_notifications_%d_%d", os.Getpid(), time.Now().UnixNano())
	if err := admin.Exec("CREATE DATABASE " + name).Error; err != nil {
		t.Fatal(err)
	}
	cfg.DBName = name
	database, err := rmdb.Connect(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		connection, _ := database.DB()
		_ = connection.Close()
		if err := admin.Exec("DROP DATABASE " + name + " WITH (FORCE)").Error; err != nil {
			t.Error(err)
		}
		connection, _ = admin.DB()
		_ = connection.Close()
	})
	if err := rmdb.AutoMigrate(database); err != nil {
		t.Fatal(err)
	}
	return database
}

func TestQuietHoursDurableSummary(t *testing.T) {
	database := quietTestDB(t)
	now := time.Date(2026, 11, 3, 2, 0, 0, 0, time.UTC)
	domain := models.Domain{Name: "quiet.example.test", Active: true}
	if err := database.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	seed := func(local string, enabled bool) models.Mailbox {
		t.Helper()
		account := models.Mailbox{DomainID: domain.ID, LocalPart: local, Address: local + "@" + domain.Name, Active: true,
			QuietHoursEnabled: enabled, QuietHoursStart: "22:00", QuietHoursEnd: "08:00", QuietHoursTimeZone: "Europe/Berlin"}
		if err := database.Create(&account).Error; err != nil {
			t.Fatal(err)
		}
		session := "quiet-" + local
		if err := database.Create(&models.RefreshToken{Jti: session, SessionID: session, AccountID: account.ID, Status: models.RefreshTokenActive, ExpiresAt: now.Add(72 * time.Hour)}).Error; err != nil {
			t.Fatal(err)
		}
		if err := database.Create(&models.PushDevice{SessionID: session, MailboxID: account.ID, Platform: models.PushPlatformAPNs, Token: testToken + local, Environment: models.APNsProduction}).Error; err != nil {
			t.Fatal(err)
		}
		return account
	}
	account := seed("held", true)
	other := seed("also-held", true)
	_ = seed("empty", true)
	normal := seed("normal", false)
	insert := func(account models.Mailbox, folder string, at time.Time) models.Message {
		t.Helper()
		message := models.Message{MailboxID: account.ID, Folder: folder, Sender: "private@example.test", Subject: "private subject", BodyText: "secret body", CreatedAt: at, ReceivedAt: at}
		if err := database.Create(&message).Error; err != nil {
			t.Fatal(err)
		}
		return message
	}
	sender := &fakeSender{sent: make(chan Notification, 32), result: Result{Status: 200}}
	dispatcher := NewDispatcher(NewStore(database), sender, time.Second)
	first := insert(account, "INBOX", now)
	insert(account, "INBOX", now.Add(time.Minute))
	insert(other, "INBOX", now)
	insert(account, "Sent", now)
	insert(normal, "INBOX", now)
	dispatcher.NewMail(account.ID)
	dispatcher.NewMail(normal.ID)
	dispatcher.Step(context.Background(), now.Add(time.Minute))
	if len(sender.sent) != 1 {
		t.Fatalf("quiet hours leaked push: %d notifications", len(sender.sent))
	}
	<-sender.sent
	var readable models.Message
	if err := database.First(&readable, first.ID).Error; err != nil || readable.BodyText != "secret body" || readable.Folder != "INBOX" {
		t.Fatalf("mail delivery/read changed: %+v, %v", readable, err)
	}
	if err := database.Model(&models.Message{}).Where("mailbox_id = ?", account.ID).Update("is_read", true).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Delete(&first).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Transaction(func(tx *gorm.DB) error {
		message := models.Message{MailboxID: account.ID, Folder: "INBOX", Sender: "rollback@example.test", CreatedAt: now}
		if err := tx.Create(&message).Error; err != nil {
			t.Fatal(err)
		}
		return fmt.Errorf("roll back delivery")
	}); err == nil {
		t.Fatal("expected delivery rollback")
	}
	dispatcher.Close()
	restarted := NewDispatcher(NewStore(database), sender, time.Second)
	replica := NewDispatcher(NewStore(database), sender, time.Second)
	restarted.Step(context.Background(), now.Add(2*time.Hour))
	if len(sender.sent) != 0 {
		t.Fatal("restart sent during quiet hours")
	}
	end := time.Date(2026, 11, 3, 7, 0, 0, 0, time.UTC)
	var workers sync.WaitGroup
	for _, worker := range []*Dispatcher{restarted, replica} {
		workers.Add(1)
		go func() { defer workers.Done(); worker.Step(context.Background(), end) }()
	}
	workers.Wait()
	if len(sender.sent) != 2 {
		t.Fatalf("want one summary for each nonempty account, got %d", len(sender.sent))
	}
	counts := map[uint]int64{}
	for len(sender.sent) > 0 {
		notification := <-sender.sent
		var payload struct {
			RestMail struct {
				Account uint  `json:"account"`
				Unread  int64 `json:"unread"`
				Arrived int64 `json:"arrived"`
			} `json:"restmail"`
		}
		if err := json.Unmarshal(notification.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		counts[payload.RestMail.Account] = payload.RestMail.Arrived
		if payload.RestMail.Account == account.ID && payload.RestMail.Unread != 0 {
			t.Fatal("summary did not use current unread count")
		}
		for _, secret := range []string{"private", "secret", "subject", "sender"} {
			if strings.Contains(string(notification.Payload), secret) {
				t.Fatalf("summary leaked message content: %s", notification.Payload)
			}
		}
	}
	if counts[account.ID] != 2 || counts[other.ID] != 1 {
		t.Fatalf("arrival counts: %v", counts)
	}
	NewDispatcher(NewStore(database), sender, time.Second).Step(context.Background(), end.Add(time.Minute))
	if len(sender.sent) != 0 {
		t.Fatal("restart sent a summary twice")
	}
	var remaining int64
	if err := database.Model(&models.QuietNotification{}).Count(&remaining).Error; err != nil || remaining != 0 {
		t.Fatalf("outbox not drained: %d, %v", remaining, err)
	}
	beforeQuiet := seed("before-quiet", true)
	before := time.Date(2026, 11, 3, 20, 59, 58, 0, time.UTC)
	coalescing := NewDispatcher(NewStore(database), sender, 5*time.Second)
	insert(beforeQuiet, "INBOX", before)
	coalescing.NewMail(beforeQuiet.ID)
	coalescing.Step(context.Background(), before)
	if len(sender.sent) != 1 {
		t.Fatal("ordinary mail before quiet hours was not notified")
	}
	<-sender.sent
	insert(beforeQuiet, "INBOX", before.Add(time.Second))
	coalescing.NewMail(beforeQuiet.ID)
	coalescing.Step(context.Background(), before.Add(time.Second))
	coalescing.Close()
	if err := database.Model(&models.QuietNotification{}).Where("mailbox_id = ?", beforeQuiet.ID).Count(&remaining).Error; err != nil || remaining != 1 {
		t.Fatalf("coalesced notification entering quiet hours was not persisted: %d, %v", remaining, err)
	}
	restartedCoalescing := NewDispatcher(NewStore(database), sender, time.Second)
	restartedCoalescing.Step(context.Background(), before.Add(time.Minute))
	if len(sender.sent) != 0 {
		t.Fatal("restarted coalesced push escaped during quiet hours")
	}
	restartedCoalescing.Step(context.Background(), end.Add(24*time.Hour))
	if len(sender.sent) != 1 {
		t.Fatal("restarted coalesced push was lost")
	}
	if notification := <-sender.sent; !strings.Contains(string(notification.Payload), `"unread":2`) {
		t.Fatalf("coalesced summary lost the ordinary unread count: %s", notification.Payload)
	}
	ordinary := NewDispatcher(NewStore(database), sender, 5*time.Second)
	ordinary.NewMail(normal.ID)
	ordinary.Step(context.Background(), now)
	if len(sender.sent) != 1 {
		t.Fatal("ordinary push was not sent")
	}
	<-sender.sent
	insert(normal, "INBOX", now.Add(time.Second))
	ordinary.NewMail(normal.ID)
	if next := ordinary.Step(context.Background(), now.Add(time.Second)); !next.Equal(now.Add(5 * time.Second)) {
		t.Fatalf("empty quiet-hours outbox replaced the ordinary deadline: %s", next)
	}
	ordinary.Step(context.Background(), now.Add(5*time.Second))
	if len(sender.sent) != 1 {
		t.Fatal("ordinary coalesced push was not sent")
	}
}
