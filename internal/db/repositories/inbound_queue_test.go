package repositories

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"gorm.io/gorm"
)

func openInboundQueue(t *testing.T) (*InboundQueue, *gorm.DB) {
	t.Helper()
	db := openCredentialTestDB(t)
	if err := db.AutoMigrate(&models.InboundMessage{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("DELETE FROM inbound_messages").Error; err != nil {
		t.Fatal(err)
	}
	return NewInboundQueue(db), db
}

func inboundMessage(ref string) *models.InboundMessage {
	return &models.InboundMessage{ObjectRef: ref, Size: 5, Checksum: "abc", MailFrom: "a@example.org", Recipients: models.JSONB(`["b@example.test"]`)}
}

func TestInboundQueueHandsOutTheOldestFirst(t *testing.T) {
	queue, _ := openInboundQueue(t)
	now := time.Now()
	for i, ref := range []string{"older", "newer"} {
		if err := queue.Enqueue(inboundMessage(ref), now.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	later := now.Add(time.Minute)
	for _, want := range []string{"older", "newer"} {
		got, err := queue.Claim(later, time.Hour)
		if err != nil || got == nil || got.ObjectRef != want {
			t.Fatalf("claimed %+v, %v, want %s", got, err, want)
		}
		if got.Status != models.InboundProcessing || got.Attempts != 1 {
			t.Fatalf("claimed %+v", got)
		}
	}
	if got, err := queue.Claim(later, time.Hour); err != nil || got != nil {
		t.Fatalf("an empty queue gave %+v, %v", got, err)
	}
}

func TestInboundQueueWaitsForNextAttempt(t *testing.T) {
	queue, _ := openInboundQueue(t)
	now := time.Now()
	if err := queue.Enqueue(inboundMessage("later"), now); err != nil {
		t.Fatal(err)
	}
	claimed, err := queue.Claim(now, time.Hour)
	if err != nil || claimed == nil {
		t.Fatalf("%+v, %v", claimed, err)
	}
	if err := queue.Retry(claimed.ID, now.Add(time.Minute), "filter unreachable"); err != nil {
		t.Fatal(err)
	}
	if got, err := queue.Claim(now.Add(time.Second), time.Hour); err != nil || got != nil {
		t.Fatalf("claimed before its time: %+v, %v", got, err)
	}
	got, err := queue.Claim(now.Add(2*time.Minute), time.Hour)
	if err != nil || got == nil || got.Attempts != 2 || got.LastError != "filter unreachable" {
		t.Fatalf("%+v, %v", got, err)
	}
}

// A worker that dies leaves its message "processing": another takes it over, but only once it
// has plainly been abandoned.
func TestInboundQueueTakesOverFromADeadWorker(t *testing.T) {
	queue, _ := openInboundQueue(t)
	now := time.Now()
	if err := queue.Enqueue(inboundMessage("abandoned"), now); err != nil {
		t.Fatal(err)
	}
	if got, err := queue.Claim(now, 10*time.Minute); err != nil || got == nil {
		t.Fatalf("%+v, %v", got, err)
	}
	if got, err := queue.Claim(now.Add(time.Minute), 10*time.Minute); err != nil || got != nil {
		t.Fatalf("taken from a live worker: %+v, %v", got, err)
	}
	got, err := queue.Claim(now.Add(11*time.Minute), 10*time.Minute)
	if err != nil || got == nil || got.Attempts != 2 {
		t.Fatalf("%+v, %v", got, err)
	}
}

func TestInboundQueueFinishAndFail(t *testing.T) {
	queue, db := openInboundQueue(t)
	now := time.Now()
	for _, ref := range []string{"done", "broken"} {
		if err := queue.Enqueue(inboundMessage(ref), now); err != nil {
			t.Fatal(err)
		}
	}
	done, _ := queue.Claim(now, time.Hour)
	broken, _ := queue.Claim(now, time.Hour)
	if err := queue.Finish(done.ID); err != nil {
		t.Fatal(err)
	}
	if err := queue.Finish(done.ID); !errors.Is(err, ErrInboundMessageNotFound) {
		t.Fatalf("finishing twice: %v", err)
	}
	if err := queue.Fail(broken.ID, "no such object"); err != nil {
		t.Fatal(err)
	}
	// A failed message is kept, and never handed out again.
	if got, err := queue.Claim(now.Add(24*time.Hour), time.Hour); err != nil || got != nil {
		t.Fatalf("%+v, %v", got, err)
	}
	var kept models.InboundMessage
	if err := db.First(&kept, broken.ID).Error; err != nil || kept.Status != models.InboundFailed || kept.LastError != "no such object" {
		t.Fatalf("%+v, %v", kept, err)
	}
}

// However many workers ask at once, each message goes to exactly one of them.
func TestInboundQueueGivesEachMessageToOneWorker(t *testing.T) {
	queue, _ := openInboundQueue(t)
	now := time.Now()
	const messages, workers = 20, 8
	for i := range messages {
		if err := queue.Enqueue(inboundMessage(time.Duration(i).String()), now); err != nil {
			t.Fatal(err)
		}
	}
	var (
		mu     sync.Mutex
		claims = map[uint]int{}
		wg     sync.WaitGroup
	)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				got, err := queue.Claim(now, time.Hour)
				if err != nil {
					t.Error(err)
					return
				}
				if got == nil {
					return
				}
				mu.Lock()
				claims[got.ID]++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(claims) != messages {
		t.Fatalf("%d messages claimed, want %d", len(claims), messages)
	}
	for id, n := range claims {
		if n != 1 {
			t.Errorf("message %d claimed %d times", id, n)
		}
	}
}
