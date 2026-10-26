package dkimrotate_test

import (
	"context"
	"sync"
	"testing"
	"time"

	dkim "github.com/rest-mail/go-dkim/pkg/dkim"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/dkimrotate"
)

// fakeDNS answers TXT lookups from what a test has "published".
type fakeDNS struct {
	mu      sync.Mutex
	records map[string][]string
}

func (d *fakeDNS) publish(name, value string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.records[name] = append(d.records[name], value)
}

func (d *fakeDNS) lookup(_ context.Context, name string) ([]string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.records[name], nil
}

// A due key is replaced one step a tick: staged, published once its record resolves,
// switched to one TTL after that, and the old key retired one TTL after the switch (#728).
func TestWorker_RotatesADueKeyThroughToRetirement(t *testing.T) {
	database := openStageTestDB(t)
	current, _ := activationMaterial(t)
	start := time.Now().UTC().Truncate(time.Second)
	installed := start.Add(-2 * 24 * time.Hour)
	domain := models.Domain{Name: "rotate.test", DKIMSelector: "default", DKIMPrivateKey: current,
		DKIMCreatedAt: &installed, DKIMRotationIntervalDays: 1}
	if err := database.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}

	dns := &fakeDNS{records: map[string][]string{}}
	clock := start
	worker := dkimrotate.NewWorker(database, stageMasterKey, dns.lookup, time.Hour)
	dkimrotate.SetWorkerClock(worker, func() time.Time { return clock })
	tick := func(at time.Time) {
		t.Helper()
		clock = at
		worker.Tick(context.Background())
	}
	slot := func(name string) *models.DKIMRotationKey {
		t.Helper()
		var keys []models.DKIMRotationKey
		if err := database.Where("domain_id = ? AND slot = ?", domain.ID, name).Find(&keys).Error; err != nil {
			t.Fatal(err)
		}
		if len(keys) == 0 {
			return nil
		}
		return &keys[0]
	}
	signing := func() models.Domain {
		t.Helper()
		var d models.Domain
		if err := database.Take(&d, domain.ID).Error; err != nil {
			t.Fatal(err)
		}
		return d
	}

	// 1. Staged; signing does not move.
	tick(start)
	staged := slot(models.DKIMRotationStaged)
	if staged == nil || staged.Selector != "restmail-1" || staged.PublishedAt != nil {
		t.Fatalf("after the first tick: staged %+v, want restmail-1 unpublished", staged)
	}
	if got := signing().DKIMSelector; got != "default" {
		t.Fatalf("signing moved to %s before the new record was published", got)
	}

	// 2. Not published yet: nothing moves, however long it waits.
	tick(start.Add(3 * time.Hour))
	if slot(models.DKIMRotationStaged).PublishedAt != nil {
		t.Fatal("publication recorded with no record in DNS")
	}

	// 3. The record resolves: publication is recorded, and the TTL starts.
	record, err := dkimrotate.PublicRecord(staged.PrivateKey, stageMasterKey)
	if err != nil {
		t.Fatal(err)
	}
	dns.publish(dkim.RecordName("restmail-1", "rotate.test"), record)
	published := start.Add(4 * time.Hour)
	tick(published)
	if at := slot(models.DKIMRotationStaged).PublishedAt; at == nil || !at.Equal(published) {
		t.Fatalf("publication recorded at %v, want %v", at, published)
	}

	// 4. Within the TTL signing waits; past it, it moves, and the old key is kept.
	tick(published.Add(dkimrotate.RecordTTL / 2))
	if got := signing().DKIMSelector; got != "default" {
		t.Fatalf("signing moved to %s within the TTL", got)
	}
	switched := published.Add(dkimrotate.RecordTTL + time.Minute)
	tick(switched)
	if got := signing().DKIMSelector; got != "restmail-1" {
		t.Fatalf("after the TTL signing is %s, want restmail-1", got)
	}
	previous := slot(models.DKIMRotationPrevious)
	if previous == nil || previous.Selector != "default" || previous.RetireAfter == nil {
		t.Fatalf("the old key was not kept: %+v", previous)
	}
	if slot(models.DKIMRotationStaged) != nil {
		t.Fatal("the staged slot outlived the switch")
	}

	// 5. The old key is let go one TTL after the switch, not before.
	tick(switched.Add(dkimrotate.RecordTTL / 2))
	if slot(models.DKIMRotationPrevious) == nil {
		t.Fatal("the old key was retired within the TTL")
	}
	tick(switched.Add(dkimrotate.RecordTTL + time.Minute))
	if slot(models.DKIMRotationPrevious) != nil {
		t.Fatal("the old key was not retired after the TTL")
	}

	// 6. The new key is young: nothing is staged until it is due.
	if slot(models.DKIMRotationStaged) != nil {
		t.Fatal("a new key was staged for a key just installed")
	}
}

// A domain with rotation off is never touched.
func TestWorker_LeavesADomainWithoutAnIntervalAlone(t *testing.T) {
	database := openStageTestDB(t)
	current, _ := activationMaterial(t)
	installed := time.Now().Add(-400 * 24 * time.Hour)
	domain := models.Domain{Name: "still.test", DKIMSelector: "default", DKIMPrivateKey: current, DKIMCreatedAt: &installed}
	if err := database.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	dns := &fakeDNS{records: map[string][]string{}}
	dkimrotate.NewWorker(database, stageMasterKey, dns.lookup, time.Hour).Tick(context.Background())
	if rows := stagedRows(t, database, domain.ID); len(rows) != 0 {
		t.Fatalf("staged %d keys for a domain with rotation off", len(rows))
	}
}
