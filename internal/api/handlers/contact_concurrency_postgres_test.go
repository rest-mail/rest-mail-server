package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/api/middleware"
	rmdb "github.com/rest-mail/rest-mail-server/internal/db"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"gorm.io/gorm"
)

// contactRaceHoldKey marks the request whose transaction the test pauses
// right after its contact write, while it still holds the row (or unique
// index) lock and has not committed.
type contactRaceHoldKey struct{}

type contactRaceHold struct {
	once    sync.Once
	pid     int
	held    chan struct{}
	release chan struct{}
}

// contactRace is a database of its own for one race: the hold callbacks it
// registers never reach the package's shared pool.
type contactRace struct {
	t       *testing.T
	db      *gorm.DB
	mailbox models.Mailbox
	router  http.Handler
}

func newContactRace(t *testing.T) *contactRace {
	t.Helper()
	if os.Getenv("DB_HOST") == "" {
		t.Skip("PostgreSQL contact concurrency proof runs in CI with DB_HOST; no local database is started")
	}
	// Migrates the account and grant tables access reads, and gives the
	// installation its default server ceiling so the owner reaches its mail.
	ensureDefaultServerCeiling(t, openAuthzTestDB(t))
	db, err := rmdb.Connect(testDBConfig())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	if err := migrateModels(db, &models.Domain{}, &models.Mailbox{}, &models.Contact{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_contacts_mailbox_email ON contacts(mailbox_id, email)`).Error; err != nil {
		t.Fatal(err)
	}
	hold := func(q *gorm.DB) {
		h, ok := q.Statement.Context.Value(contactRaceHoldKey{}).(*contactRaceHold)
		if !ok || q.Statement.Table != "contacts" || q.Error != nil {
			return
		}
		h.once.Do(func() {
			if err := q.Session(&gorm.Session{NewDB: true}).Raw("SELECT pg_backend_pid()").Scan(&h.pid).Error; err != nil {
				_ = q.AddError(err)
			}
			close(h.held)
			<-h.release
		})
	}
	for _, register := range []func() error{
		func() error { return db.Callback().Update().After("gorm:update").Register("contact_race_hold", hold) },
		func() error { return db.Callback().Delete().After("gorm:delete").Register("contact_race_hold", hold) },
		func() error { return db.Callback().Create().After("gorm:create").Register("contact_race_hold", hold) },
	} {
		if err := register(); err != nil {
			t.Fatal(err)
		}
	}

	// Each race commits real rows, so it creates its own domain and mailbox
	// and removes them afterwards.
	suffix := time.Now().UnixNano()
	domain := models.Domain{Name: fmt.Sprintf("race-%d.test", suffix)}
	if err := db.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	mailbox := models.Mailbox{DomainID: domain.ID, LocalPart: "racer", Address: "racer@" + domain.Name, Active: true}
	if err := db.Create(&mailbox).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Where("mailbox_id = ?", mailbox.ID).Delete(&models.Contact{})
		db.Delete(&models.Mailbox{}, mailbox.ID)
		db.Delete(&models.Domain{}, domain.ID)
	})
	return &contactRace{t: t, db: db, mailbox: mailbox, router: contactFaultRouter(db)}
}

func (rc *contactRace) contact(email string) models.Contact {
	rc.t.Helper()
	c := models.Contact{MailboxID: rc.mailbox.ID, Email: email, Name: "Original", TrustLevel: "trusted", Source: "manual"}
	if err := rc.db.Create(&c).Error; err != nil {
		rc.t.Fatal(err)
	}
	return c
}

type contactCall struct{ method, path, body string }

func (rc *contactRace) patch(c models.Contact, body string) contactCall {
	return contactCall{"PATCH", fmt.Sprintf("/api/v1/accounts/%d/contacts/%d", rc.mailbox.ID, c.ID), body}
}

func (rc *contactRace) remove(c models.Contact) contactCall {
	return contactCall{"DELETE", fmt.Sprintf("/api/v1/accounts/%d/contacts/%d", rc.mailbox.ID, c.ID), ""}
}

func (rc *contactRace) block(email string) contactCall {
	return contactCall{"POST", fmt.Sprintf("/api/v1/accounts/%d/contacts/block", rc.mailbox.ID), fmt.Sprintf(`{"email":%q}`, email)}
}

func (rc *contactRace) serve(ctx context.Context, call contactCall) <-chan *httptest.ResponseRecorder {
	r := authzRequest(call.method, call.path, rc.mailbox.ID, nil, call.body)
	claims := middleware.GetClaims(r)
	claims.CredentialID = 11
	claims.CredentialKind = "app_password"
	r = r.WithContext(context.WithValue(ctx, middleware.ClaimsKey, claims))
	r.Header.Set("Content-Type", "application/json")
	out := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rr := httptest.NewRecorder()
		rc.router.ServeHTTP(rr, r)
		out <- rr
	}()
	return out
}

// race runs first until it has written and holds its lock, starts second,
// waits until PostgreSQL reports second blocked by first's backend, checks
// second has not answered, then lets first commit. The order is fixed by
// the lock, not by timing.
func (rc *contactRace) race(first, second contactCall) (*httptest.ResponseRecorder, *httptest.ResponseRecorder) {
	rc.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	h := &contactRaceHold{held: make(chan struct{}), release: make(chan struct{})}
	released := false
	defer func() {
		if !released {
			close(h.release)
		}
	}()
	firstDone := rc.serve(context.WithValue(ctx, contactRaceHoldKey{}, h), first)
	select {
	case <-h.held:
	case rr := <-firstDone:
		rc.t.Fatalf("first request finished without writing: status=%d body=%s", rr.Code, rr.Body)
	case <-ctx.Done():
		rc.t.Fatal("first request never reached its write")
	}
	if h.pid == 0 {
		rc.t.Fatal("first request's backend pid is unknown")
	}
	secondDone := rc.serve(ctx, second)
	for {
		var blocked int64
		if err := rc.db.Raw(`SELECT count(*) FROM pg_stat_activity WHERE ? = ANY(pg_blocking_pids(pid))`, h.pid).Scan(&blocked).Error; err != nil {
			rc.t.Fatal(err)
		}
		if blocked > 0 {
			break
		}
		select {
		case rr := <-secondDone:
			rc.t.Fatalf("second request was not serialised behind the first's lock: status=%d body=%s", rr.Code, rr.Body)
		case <-ctx.Done():
			rc.t.Fatal("second request never waited on the first's lock")
		case <-time.After(5 * time.Millisecond):
		}
	}
	select {
	case rr := <-secondDone:
		rc.t.Fatalf("second request answered while the first held the lock: status=%d body=%s", rr.Code, rr.Body)
	default:
	}
	close(h.release)
	released = true
	var a, b *httptest.ResponseRecorder
	select {
	case a = <-firstDone:
	case <-ctx.Done():
		rc.t.Fatal("first request did not finish")
	}
	select {
	case b = <-secondDone:
	case <-ctx.Done():
		rc.t.Fatal("second request did not finish")
	}
	return a, b
}

func (rc *contactRace) stored(id uint) (models.Contact, bool) {
	rc.t.Helper()
	var c models.Contact
	err := rc.db.First(&c, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return c, false
	}
	if err != nil {
		rc.t.Fatal(err)
	}
	return c, true
}

func (rc *contactRace) byEmail(email string) []models.Contact {
	rc.t.Helper()
	var cs []models.Contact
	if err := rc.db.Where("mailbox_id = ? AND email = ?", rc.mailbox.ID, email).Find(&cs).Error; err != nil {
		rc.t.Fatal(err)
	}
	return cs
}

func wantContactAnswer(t *testing.T, who string, rr *httptest.ResponseRecorder, status int, name, trust string) Contact {
	t.Helper()
	if rr.Code != status {
		t.Fatalf("%s: status=%d want %d body=%s", who, rr.Code, status, rr.Body)
	}
	var envelope struct {
		Data Contact `json:"data"`
	}
	if status == http.StatusNoContent || status == http.StatusNotFound {
		return envelope.Data
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("%s: %v body=%s", who, err, rr.Body)
	}
	c := envelope.Data
	if c.Name != name || c.TrustLevel != trust {
		t.Fatalf("%s: answered %+v, want name=%q trust=%q", who, c, name, trust)
	}
	return c
}

func wantStoredContact(t *testing.T, rc *contactRace, original models.Contact, name, trust string) {
	t.Helper()
	got, ok := rc.stored(original.ID)
	if !ok {
		t.Fatalf("contact %d vanished", original.ID)
	}
	if got.Name != name || got.TrustLevel != trust || got.Email != original.Email || got.MailboxID != original.MailboxID {
		t.Fatalf("stored %+v, want name=%q trust=%q email=%q", got, name, trust, original.Email)
	}
}

func TestContactPostgresConcurrentUpdates(t *testing.T) {
	t.Run("different fields: neither is lost", func(t *testing.T) {
		rc := newContactRace(t)
		c := rc.contact("fields@example.test")
		a, b := rc.race(rc.patch(c, `{"name":"First"}`), rc.patch(c, `{"trust_level":"blocked"}`))
		wantContactAnswer(t, "first", a, 200, "First", "trusted")
		// The second writer reloads after the first commits, so its answer
		// already carries the first writer's name.
		wantContactAnswer(t, "second", b, 200, "First", "blocked")
		wantStoredContact(t, rc, c, "First", "blocked")
	})
	t.Run("same field: last committed wins", func(t *testing.T) {
		rc := newContactRace(t)
		c := rc.contact("same@example.test")
		a, b := rc.race(rc.patch(c, `{"name":"First","trust_level":"auto"}`), rc.patch(c, `{"name":"Second","trust_level":"blocked"}`))
		wantContactAnswer(t, "first", a, 200, "First", "auto")
		wantContactAnswer(t, "second", b, 200, "Second", "blocked")
		wantStoredContact(t, rc, c, "Second", "blocked")
	})
}

func TestContactPostgresConcurrentUpdateAndDelete(t *testing.T) {
	t.Run("delete first: update gets 404, row stays gone", func(t *testing.T) {
		rc := newContactRace(t)
		c := rc.contact("gone@example.test")
		a, b := rc.race(rc.remove(c), rc.patch(c, `{"name":"Resurrected"}`))
		wantContactAnswer(t, "delete", a, 204, "", "")
		wantContactAnswer(t, "update", b, 404, "", "")
		if got, ok := rc.stored(c.ID); ok {
			t.Fatalf("deleted contact resurrected: %+v", got)
		}
		if cs := rc.byEmail(c.Email); len(cs) != 0 {
			t.Fatalf("contact rows remain after delete: %+v", cs)
		}
	})
	t.Run("update first: delete waits and removes the updated row", func(t *testing.T) {
		rc := newContactRace(t)
		c := rc.contact("later@example.test")
		a, b := rc.race(rc.patch(c, `{"name":"Changed"}`), rc.remove(c))
		wantContactAnswer(t, "update", a, 200, "Changed", "trusted")
		wantContactAnswer(t, "delete", b, 204, "", "")
		if got, ok := rc.stored(c.ID); ok {
			t.Fatalf("contact survived delete: %+v", got)
		}
	})
}

func TestContactPostgresConcurrentBlock(t *testing.T) {
	t.Run("block first: update keeps the block", func(t *testing.T) {
		rc := newContactRace(t)
		c := rc.contact("spam@example.test")
		a, b := rc.race(rc.block(c.Email), rc.patch(c, `{"name":"Renamed"}`))
		got := wantContactAnswer(t, "block", a, 200, "Original", "blocked")
		if got.ID != c.ID {
			t.Fatalf("block answered contact %d, want %d", got.ID, c.ID)
		}
		wantContactAnswer(t, "update", b, 200, "Renamed", "blocked")
		wantStoredContact(t, rc, c, "Renamed", "blocked")
	})
	t.Run("update first: block wins the trust level", func(t *testing.T) {
		rc := newContactRace(t)
		c := rc.contact("spam2@example.test")
		a, b := rc.race(rc.patch(c, `{"name":"Renamed","trust_level":"trusted"}`), rc.block(c.Email))
		wantContactAnswer(t, "update", a, 200, "Renamed", "trusted")
		wantContactAnswer(t, "block", b, 200, "Renamed", "blocked")
		wantStoredContact(t, rc, c, "Renamed", "blocked")
	})
	t.Run("delete first: block creates a fresh blocked contact", func(t *testing.T) {
		rc := newContactRace(t)
		c := rc.contact("spam3@example.test")
		a, b := rc.race(rc.remove(c), rc.block(c.Email))
		wantContactAnswer(t, "delete", a, 204, "", "")
		got := wantContactAnswer(t, "block", b, 201, "", "blocked")
		if got.ID == c.ID {
			t.Fatalf("block revived deleted contact %d", c.ID)
		}
		if _, ok := rc.stored(c.ID); ok {
			t.Fatalf("deleted contact %d resurrected", c.ID)
		}
		if cs := rc.byEmail(c.Email); len(cs) != 1 || cs[0].TrustLevel != "blocked" || cs[0].ID != got.ID {
			t.Fatalf("rows after delete and block: %+v", cs)
		}
	})
	t.Run("new sender blocked twice: one row, no error", func(t *testing.T) {
		rc := newContactRace(t)
		const email = "newspam@example.test"
		a, b := rc.race(rc.block(email), rc.block(email))
		first := wantContactAnswer(t, "first block", a, 201, "", "blocked")
		second := wantContactAnswer(t, "second block", b, 200, "", "blocked")
		if first.ID != second.ID {
			t.Fatalf("blocks answered different contacts %d and %d", first.ID, second.ID)
		}
		if cs := rc.byEmail(email); len(cs) != 1 || cs[0].TrustLevel != "blocked" {
			t.Fatalf("rows after two blocks: %+v", cs)
		}
	})
}

func TestContactPostgresConcurrentDeletes(t *testing.T) {
	rc := newContactRace(t)
	c := rc.contact("twice@example.test")
	a, b := rc.race(rc.remove(c), rc.remove(c))
	wantContactAnswer(t, "first delete", a, 204, "", "")
	wantContactAnswer(t, "second delete", b, 404, "", "")
	if got, ok := rc.stored(c.ID); ok {
		t.Fatalf("contact survived two deletes: %+v", got)
	}
}
