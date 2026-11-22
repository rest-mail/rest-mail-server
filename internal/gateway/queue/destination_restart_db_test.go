package queue

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/rest-mail/go-mtasts/pkg/mtasts"
	"github.com/rest-mail/rest-mail-server/internal/config"
	rmdb "github.com/rest-mail/rest-mail-server/internal/db"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"gorm.io/gorm"
)

const queueRestartReclaim = 15 * time.Second

func TestAC03StalledDestinationCannotMonopolizeQueueAcrossRestart(t *testing.T) {
	if database := os.Getenv("RESTMAIL_TEST_QUEUE_RESTART_DB"); database != "" {
		runQueueRestartWorker(t, database)
		return
	}

	db := openResetWorkerDB(t)
	migrateDeliveryTables(t, db)
	_, sender := sendingDomain(t, db)
	const slowDomain = "stalled.test"
	stall := newTimedMX(t, timedMXOptions{hold: "MAIL"})
	peers := map[string]string{slowDomain: stall.addr("")}
	now := time.Now()
	slow := queueFor(t, db, sender.Address, slowDomain, now.Add(-time.Hour))
	var held []models.OutboundQueue
	for index := range 4 {
		row := queueFor(t, db, sender.Address, slowDomain, now.Add(-time.Hour+time.Duration(index+1)*time.Second))
		held = append(held, readRow(t, db, row.ID))
	}
	fast := map[string]*timedMX{}
	var other []models.OutboundQueue
	for _, domain := range []string{"first.test", "second.test", "third.test"} {
		peer := newTimedMX(t, timedMXOptions{hold: "MAIL"})
		fast[domain], peers[domain] = peer, peer.addr("")
		for index := range 2 {
			other = append(other, queueFor(t, db, sender.Address, domain, now.Add(-time.Minute+time.Duration(index)*time.Second)))
		}
	}

	crash := startQueueRestartProcess(t, db, peers)
	for _, domain := range []string{slowDomain, "first.test", "second.test", "third.test"} {
		peer := fast[domain]
		if domain == slowDomain {
			peer = stall
		}
		deadline := time.Now().Add(8 * time.Second)
		for peer.stalledSessions() == 0 && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if peer.stalledSessions() == 0 || stall.stalledSessions() != 1 {
			t.Fatalf("destination %s did not occupy one worker alongside the stalled peer: %d sessions there, %d at stalled peer (cap 1)", domain, peer.stalledSessions(), stall.stalledSessions())
		}
	}
	for _, peer := range fast {
		peer.releaseHold()
	}
	waitUntil(t, "all six fast deliveries while the slow session is still stalled", func() bool {
		for _, row := range other {
			if readRow(t, db, row.ID).Status != "delivered" {
				return false
			}
		}
		return true
	})
	for domain, peer := range fast {
		if got := peer.copiesReceived(); got != 2 {
			t.Errorf("%s got %d copies while %s stalled, want 2", domain, got, slowDomain)
		}
	}
	assertQueueRestartHeld(t, db, held)
	crash()
	if orphan := readRow(t, db, slow.ID); orphan.Status != "delivering" || orphan.Attempts != 1 || orphan.LastAttempt == nil || stall.copiesReceived() != 0 {
		t.Fatalf("crashed worker left no undelivered reservation to reclaim: %+v; %d slow copies received", orphan, stall.copiesReceived())
	}

	postCrash := queueFor(t, db, sender.Address, "first.test", time.Now().Add(-time.Second))
	restarted := newQueueRestartWorker(db, peers)
	restarted.Start()
	t.Cleanup(restarted.Shutdown)
	t.Cleanup(stall.releaseHold)
	waitUntil(t, "a fresh fast row to deliver on the replacement worker", func() bool {
		return readRow(t, db, postCrash.ID).Status == "delivered"
	})
	assertQueueRestartHeld(t, db, held)
	stall.releaseHold()
	waitUntil(t, "the replacement worker to reclaim the crash and drain the slow destination", func() bool {
		if readRow(t, db, slow.ID).Status != "delivered" {
			return false
		}
		for _, row := range held {
			if readRow(t, db, row.ID).Status != "delivered" {
				return false
			}
		}
		return true
	})
	if orphan := readRow(t, db, slow.ID); orphan.Attempts != 2 {
		t.Errorf("crashed row ended with %d attempts, want one abandoned attempt and one delivery", orphan.Attempts)
	}
	for _, row := range append(append(other, postCrash), held...) {
		if got := readRow(t, db, row.ID); got.Status != "delivered" || got.Attempts != 1 {
			t.Errorf("row %d ended %s after %d attempts, want one delivery attempt", row.ID, got.Status, got.Attempts)
		}
	}
	if got := stall.copiesReceived(); got != len(held)+1 {
		t.Errorf("slow peer got %d copies after restart, want %d", got, len(held)+1)
	}
}

func assertQueueRestartHeld(t *testing.T, db *gorm.DB, held []models.OutboundQueue) {
	t.Helper()
	for _, before := range held {
		got := readRow(t, db, before.ID)
		if got.Status != "pending" || got.Attempts != before.Attempts || got.LastAttempt != nil ||
			!got.NextAttempt.Equal(before.NextAttempt) || got.LastError != before.LastError {
			t.Fatalf("held row %d changed: %s, attempts %d, last attempt %v, next attempt %s (was %s), error %q",
				got.ID, got.Status, got.Attempts, got.LastAttempt, got.NextAttempt, before.NextAttempt, got.LastError)
		}
	}
}

func newQueueRestartWorker(db *gorm.DB, peers map[string]string) *Worker {
	worker := NewWorker(db, "sender.test", 4, 20*time.Millisecond)
	worker.resolver = destinationMXes{}
	worker.SetSMTPAddress(func(host string) string { return peers[host] })
	worker.SetAllowPrivateDestinations(true)
	worker.sts.LookupTXT = func(context.Context, string) ([]string, error) { return nil, mtasts.ErrNoPolicy }
	worker.smtpTimeouts = everyStep(12 * time.Second)
	worker.SetDeliveryDeadline(nil, queueRestartReclaim)
	worker.heartbeatEvery = 250 * time.Millisecond
	worker.SetDestinationLimit(1)
	return worker
}

func startQueueRestartProcess(t *testing.T, db *gorm.DB, peers map[string]string) func() {
	t.Helper()
	var database string
	if err := db.Raw("SELECT current_database()").Scan(&database).Error; err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(peers)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	cmd := exec.Command(os.Args[0], "-test.run=^TestAC03StalledDestinationCannotMonopolizeQueueAcrossRestart$", "-test.timeout=30m")
	cmd.Env = append(os.Environ(), "RESTMAIL_TEST_QUEUE_RESTART_DB="+database, "RESTMAIL_TEST_QUEUE_RESTART_PEERS="+string(encoded))
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	crash := func() {
		once.Do(func() {
			killErr, waitErr := cmd.Process.Kill(), cmd.Wait()
			if killErr != nil || cmd.ProcessState == nil || cmd.ProcessState.Exited() {
				t.Errorf("worker did not crash as requested (kill %v, wait %v):\n%s", killErr, waitErr, &output)
			} else if t.Failed() {
				t.Logf("crashed worker output:\n%s", &output)
			}
		})
	}
	t.Cleanup(crash)
	return crash
}

func runQueueRestartWorker(t *testing.T, database string) {
	t.Helper()
	var peers map[string]string
	if err := json.Unmarshal([]byte(os.Getenv("RESTMAIL_TEST_QUEUE_RESTART_PEERS")), &peers); err != nil {
		t.Fatal(err)
	}
	db, err := rmdb.Connect(&config.Config{DBHost: envOr("DB_HOST", "localhost"), DBPort: envIntOr("DB_PORT", 5432),
		DBName: database, DBUser: envOr("DB_USER", "restmail"), DBPass: envOr("DB_PASS", "restmail"), DBMaxIdleConns: 5})
	if err != nil {
		t.Fatal(err)
	}
	newQueueRestartWorker(db, peers).Start()
	select {}
}
