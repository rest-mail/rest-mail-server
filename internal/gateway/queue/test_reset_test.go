package queue

import (
	"context"
	"fmt"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/config"
	rmdb "github.com/rest-mail/rest-mail-server/internal/db"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/testdb"
	"github.com/rest-mail/rest-mail-server/internal/testreset"
	"gorm.io/gorm"
)

func openResetWorkerDB(t *testing.T) *gorm.DB {
	t.Helper()
	cfg := &config.Config{DBHost: envOr("DB_HOST", "localhost"), DBPort: envIntOr("DB_PORT", 5432),
		DBName: "postgres", DBUser: envOr("DB_USER", "restmail"), DBPass: envOr("DB_PASS", "restmail")}
	admin, err := rmdb.Connect(cfg)
	if err != nil {
		testdb.Unreachable(t, "test reset worker database unavailable: %v", err)
	}
	adminPool, err := admin.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adminPool.Close() })
	name := fmt.Sprintf("restmail_reset_worker_%d_%d", os.Getpid(), time.Now().UnixNano())
	if err := admin.Exec("CREATE DATABASE " + name).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := admin.Exec("DROP DATABASE " + name).Error; err != nil {
			t.Error(err)
		}
	})
	cfg.DBName = name
	database, err := rmdb.Connect(cfg)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	var current string
	if err := database.Raw("SELECT current_database()").Scan(&current).Error; err != nil || current != name {
		t.Fatalf("scratch database = %q: %v", current, err)
	}
	if err := database.AutoMigrate(&models.Domain{}, &models.Mailbox{}, &models.Message{}, &models.OutboundQueue{}, &models.Suppression{}, &models.DomainRelay{}); err != nil {
		t.Fatal(err)
	}
	return database
}

type resetDeliveryResolver struct {
	claimed chan struct{}
	release chan struct{}
}

func (r *resetDeliveryResolver) LookupMX(context.Context, string) ([]*net.MX, error) {
	close(r.claimed)
	<-r.release
	return nil, &net.DNSError{Err: "no such host", Name: "reset.invalid", IsNotFound: true}
}

func (r *resetDeliveryResolver) LookupHost(context.Context, string) ([]string, error) {
	return nil, &net.DNSError{Err: "no such host", IsNotFound: true}
}

func TestTestResetWaitsForClaimedDeliverySideEffects(t *testing.T) {
	db := openResetWorkerDB(t)
	domain, sender := sendingDomain(t, db)
	now := time.Now()
	item := models.OutboundQueue{Sender: sender.Address, Recipient: "nobody@reset.invalid", Domain: "reset.invalid",
		RawMessage: "From: " + sender.Address + "\r\nTo: nobody@reset.invalid\r\nSubject: reset\r\n\r\nreset\r\n",
		Status:     "pending", NextAttempt: now.Add(-time.Second), ExpiresAt: now.Add(time.Hour), MaxRetries: 10}
	if err := db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	resolver := &resetDeliveryResolver{claimed: make(chan struct{}), release: make(chan struct{})}
	var finish sync.Once
	t.Cleanup(func() { finish.Do(func() { close(resolver.release) }) })
	worker := NewWorker(db, "reset.test", 1, time.Second)
	worker.EnableTestReset(&config.Config{Environment: "test"})
	worker.resolver = resolver
	worker.SetSuppressionTTL(time.Hour)
	delivered := make(chan struct{})
	go func() { worker.processOne(0); close(delivered) }()
	select {
	case <-resolver.claimed:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not claim the real queue row")
	}
	var claimed models.OutboundQueue
	if err := db.First(&claimed, item.ID).Error; err != nil || claimed.Status != "delivering" {
		t.Fatalf("claim not committed before delivery: %s, %v", claimed.Status, err)
	}
	resetDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		resetDone <- testreset.Reset(ctx, db, func(conn *gorm.DB) error {
			var suppressions int64
			if err := conn.Model(&models.Suppression{}).Where("domain_id = ?", domain.ID).Count(&suppressions).Error; err != nil {
				return err
			}
			if suppressions != 1 {
				t.Error("reset entered before the claimed delivery's suppression side effect finished")
			}
			return conn.Where("domain_id = ?", domain.ID).Delete(&models.Suppression{}).Error
		})
	}()
	select {
	case err := <-resetDone:
		t.Fatalf("reset returned during claimed delivery: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	finish.Do(func() { close(resolver.release) })
	select {
	case <-delivered:
	case <-time.After(5 * time.Second):
		t.Fatal("worker failed to complete")
	}
	if err := <-resetDone; err != nil {
		t.Fatal(err)
	}
	if _, exists := suppressionOf(t, db, domain.ID, item.Recipient); exists {
		t.Fatal("delivery suppression appeared after reset")
	}
}

func TestTestResetWorkerProductionGuard(t *testing.T) {
	for _, environment := range []string{"production", "prod", " Prod-eu "} {
		worker := &Worker{}
		worker.EnableTestReset(&config.Config{Environment: environment})
		if worker.testReset {
			t.Fatalf("production worker enabled reset barrier: %q", environment)
		}
	}
}
