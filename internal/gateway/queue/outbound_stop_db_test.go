package queue

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
)

// A domain whose outbound mail an operator stopped has its mail held (#1590): what
// it had queued before the stop and what it queues while stopped both stay queued,
// neither delivered nor bounced, and are claimed again once the stop is lifted.
// The rows are due a century ago, so an unheld one is the first any claim takes.
func TestStoppedDomainOutboundMailIsHeldUntilRestart(t *testing.T) {
	gdb := openFailureTestDB(t)
	if err := gdb.AutoMigrate(&models.Domain{}); err != nil {
		t.Skipf("outbound stop DB test skipped: migrate failed (%v)", err)
	}
	w := NewWorker(gdb, "mail.local.test", 1, time.Second)
	now := time.Now()
	name := fmt.Sprintf("stopped-%d.test", now.UnixNano())
	domain := models.Domain{Name: name}
	if err := gdb.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { gdb.Delete(&models.Domain{}, domain.ID) })

	enqueue := func(sender, sendingDomain, status string, due time.Time) models.OutboundQueue {
		t.Helper()
		item := models.OutboundQueue{Sender: sender, SendingDomain: sendingDomain, Recipient: "someone@remote.test", Domain: "remote.test",
			RawMessage: "Subject: hi\r\n\r\nhi\r\n", Status: status, NextAttempt: due}
		if err := gdb.Create(&item).Error; err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { gdb.Delete(&models.OutboundQueue{}, item.ID) })
		return item
	}
	longAgo := now.AddDate(-100, 0, 0)
	queuedBefore := enqueue("Owner@"+name, "", "deferred", longAgo)

	if err := gdb.Model(&domain).Update("outbound_stopped_at", now).Error; err != nil {
		t.Fatal(err)
	}
	sentWhileStopped := enqueue("owner@"+strings.ToUpper(name), "", "pending", longAgo.Add(time.Second))
	dsnWhileStopped := enqueue("", name, "pending", longAgo.Add(2*time.Second))
	held := map[uint]bool{queuedBefore.ID: true, sentWhileStopped.ID: true, dsnWhileStopped.ID: true}

	if item, err := w.claim(now); err == nil && held[item.ID] {
		t.Fatalf("claimed %d while its domain's outbound mail was stopped", item.ID)
	} else if err == nil {
		// Another package's row: give it back as it was.
		gdb.Model(&models.OutboundQueue{}).Where("id = ?", item.ID).Updates(map[string]any{"status": "pending", "attempts": item.Attempts - 1})
	}
	for id := range held {
		var got models.OutboundQueue
		if err := gdb.First(&got, id).Error; err != nil {
			t.Fatalf("held mail %d is gone: %v", id, err)
		}
		if got.Status != "pending" && got.Status != "deferred" || got.Attempts != 0 {
			t.Fatalf("held mail %d: status %q after %d attempts; want it queued and untried", id, got.Status, got.Attempts)
		}
	}

	if err := gdb.Model(&domain).Update("outbound_stopped_at", nil).Error; err != nil {
		t.Fatal(err)
	}
	for _, want := range []uint{queuedBefore.ID, sentWhileStopped.ID, dsnWhileStopped.ID} {
		item, err := w.claim(now)
		if err != nil || item.ID != want || item.Status != "delivering" {
			t.Fatalf("after the restart claimed %d (%v, %q), want %d delivering", item.ID, err, item.Status, want)
		}
	}
}
