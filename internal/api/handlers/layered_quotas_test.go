package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/db/repositories"
	"github.com/restmail/restmail/internal/grants"
	"gorm.io/gorm"
)

func createQuotaAccount(h *AccountHandler, owner uint, address, extra string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/accounts", strings.NewReader(fmt.Sprintf(`{"address":%q,"owned":true%s}`, address, extra)))
	r = r.WithContext(context.WithValue(r.Context(), middleware.ClaimsKey, &auth.Claims{AccountID: owner, AMR: []string{auth.AMRPassword, auth.AMROTP}, TwoFactor: true}))
	rr := httptest.NewRecorder()
	accountRoutes(h).ServeHTTP(rr, r)
	return rr
}

func TestOwnedQuotaCreationAndUpdate(t *testing.T) {
	db := openAccountsTestDB(t)
	fx := seedAccountsFixture(t, db)
	if err := db.Model(&fx.admin).Update("quota_bytes", 100).Error; err != nil {
		t.Fatal(err)
	}
	h := NewAccountHandler(db, grants.NewCache(db, grants.CacheTTL), "")
	rr := createQuotaAccount(h, fx.admin.ID, "inherit@"+fx.domain.Name, "")
	if rr.Code != http.StatusCreated {
		t.Fatalf("inherit: %d %s", rr.Code, rr.Body.String())
	}
	child := decodeAccount(t, rr)
	var stored models.Mailbox
	if err := db.First(&stored, child.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.QuotaBytes != 0 {
		t.Errorf("inherited quota stored as %d, want 0 (live inheritance)", stored.QuotaBytes)
	}
	for _, cap := range []int{50, 100, 101} {
		rr := createQuotaAccount(h, fx.admin.ID, fmt.Sprintf("cap%d@%s", cap, fx.domain.Name), fmt.Sprintf(`,"quota_bytes":%d`, cap))
		want := http.StatusCreated
		if cap > 100 {
			want = http.StatusUnprocessableEntity
		}
		if rr.Code != want {
			t.Errorf("create cap %d: %d %s, want %d", cap, rr.Code, rr.Body.String(), want)
		}
	}
	mh := NewMailboxHandler(db, nil, nil)
	for _, cap := range []int{101, 50, 0, -1} {
		rr := httptest.NewRecorder()
		mailboxRoutes(mh).ServeHTTP(rr, httptest.NewRequest(http.MethodPatch, fmt.Sprintf("/api/v1/admin/mailboxes/%d", child.ID), strings.NewReader(fmt.Sprintf(`{"quota_bytes":%d}`, cap))))
		want := http.StatusOK
		if cap > 100 {
			want = http.StatusUnprocessableEntity
		}
		if rr.Code != want {
			t.Errorf("update cap %d: %d %s, want %d", cap, rr.Code, rr.Body.String(), want)
		}
		if want == http.StatusOK {
			if err := db.First(&stored, child.ID).Error; err != nil {
				t.Fatal(err)
			}
			if stored.QuotaBytes != int64(cap) {
				t.Errorf("stored quota = %d, want %d", stored.QuotaBytes, cap)
			}
			chain, err := repositories.StorageQuotaChain(db, child.ID, false)
			if err != nil {
				t.Fatal(err)
			}
			ceiling := int64(cap)
			if cap <= 0 {
				ceiling = 100
			}
			if got := repositories.StorageQuotaCeiling(chain); got != ceiling {
				t.Errorf("effective ceiling = %d, want %d", got, ceiling)
			}
		}
	}
}

func quotaChild(t *testing.T, db *gorm.DB, owner models.Mailbox, local string, quota, used int64) models.Mailbox {
	t.Helper()
	mb := models.Mailbox{DomainID: owner.DomainID, LocalPart: local, Address: local + "@" + strings.SplitN(owner.Address, "@", 2)[1], OwnerID: &owner.ID, QuotaBytes: quota, QuotaUsedBytes: used, Active: true}
	if err := db.Create(&mb).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&mb).Update("quota_bytes", quota).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.QuotaUsage{MailboxID: mb.ID}).Error; err != nil {
		t.Fatal(err)
	}
	return mb
}

func deliverQuotaMessage(db *gorm.DB, id uint, subject string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/messages/deliver", strings.NewReader(fmt.Sprintf(`{"mailbox_id":%d,"sender":"sender@remote.test","subject":%q,"folder":"Archive"}`, id, subject)))
	rr := httptest.NewRecorder()
	deliveryRoute(NewMessageHandler(db, nil, nil, "", nil))(rr, r)
	return rr
}

func TestOwnedQuotaSharedDeliveryBudget(t *testing.T) {
	db := openAccountsTestDB(t)
	fx := seedAccountsFixture(t, db)
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })
	if err := tx.Model(&fx.admin).Updates(map[string]any{"quota_bytes": 100, "quota_used_bytes": 60}).Error; err != nil {
		t.Fatal(err)
	}
	a := quotaChild(t, tx, fx.admin, "a", 0, 20)
	b := quotaChild(t, tx, fx.admin, "b", 0, 10)
	grandchild := quotaChild(t, tx, a, "grandchild", 0, 0)
	for _, target := range []uint{a.ID, b.ID, fx.admin.ID, grandchild.ID} {
		rr := deliverQuotaMessage(tx, target, strings.Repeat("x", 11))
		if rr.Code != http.StatusUnprocessableEntity || errorCode(rr) != "mailbox_full" {
			t.Errorf("shared budget %d: %d %s", target, rr.Code, rr.Body.String())
		}
	}
	if rr := deliverQuotaMessage(tx, a.ID, strings.Repeat("x", 10)); rr.Code != http.StatusCreated {
		t.Fatalf("exact boundary: %d %s", rr.Code, rr.Body.String())
	}
	if rr := deliverQuotaMessage(tx, b.ID, "x"); rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("sibling budget: %d %s", rr.Code, rr.Body.String())
	}
	// Reclaimed child storage immediately restores the owner's allowance.
	if err := tx.Model(&a).Update("quota_used_bytes", 20).Error; err != nil {
		t.Fatal(err)
	}
	if rr := deliverQuotaMessage(tx, b.ID, "x"); rr.Code != http.StatusCreated {
		t.Fatalf("reclaimed: %d %s", rr.Code, rr.Body.String())
	}
	// Lowering the owner restricts already-created children live.
	if err := tx.Model(&fx.admin).Update("quota_bytes", 90).Error; err != nil {
		t.Fatal(err)
	}
	if rr := deliverQuotaMessage(tx, a.ID, "x"); rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("live ceiling: %d %s", rr.Code, rr.Body.String())
	}
}

func TestOwnedQuotaConcurrentAppends(t *testing.T) {
	db := openAccountsTestDB(t)
	fx := seedAccountsFixture(t, db)
	if err := db.Model(&fx.admin).Updates(map[string]any{"quota_bytes": 10, "quota_used_bytes": 0}).Error; err != nil {
		t.Fatal(err)
	}
	a := quotaChild(t, db, fx.admin, "a", 0, 0)
	b := quotaChild(t, db, fx.admin, "b", 0, 0)
	t.Cleanup(func() { db.Where("mailbox_id IN ?", []uint{a.ID, b.ID}).Delete(&models.Message{}) })
	start := make(chan struct{})
	results := make(chan *httptest.ResponseRecorder, 2)
	var wg sync.WaitGroup
	for _, id := range []uint{a.ID, b.ID} {
		wg.Add(1)
		go func(id uint) { defer wg.Done(); <-start; results <- deliverQuotaMessage(db, id, "123456") }(id)
	}
	close(start)
	wg.Wait()
	close(results)
	created, refused := 0, 0
	for rr := range results {
		switch rr.Code {
		case 201:
			created++
		case 422:
			refused++
		default:
			t.Errorf("append: %d %s", rr.Code, rr.Body.String())
		}
	}
	if created != 1 || refused != 1 {
		t.Fatalf("created %d, refused %d; want one each", created, refused)
	}
}

func TestOwnedAccountCountLimit(t *testing.T) {
	db := openAccountsTestDB(t)
	fx := seedAccountsFixture(t, db)
	h := NewAccountHandler(db, grants.NewCache(db, grants.CacheTTL), "", 1)
	start := make(chan struct{})
	results := make(chan *httptest.ResponseRecorder, 2)
	var wg sync.WaitGroup
	for _, local := range []string{"agent1", "agent2"} {
		wg.Add(1)
		go func(local string) {
			defer wg.Done()
			<-start
			results <- createQuotaAccount(h, fx.admin.ID, local+"@"+fx.domain.Name, "")
		}(local)
	}
	close(start)
	wg.Wait()
	close(results)
	created, refused := 0, 0
	for rr := range results {
		switch rr.Code {
		case http.StatusCreated:
			created++
		case http.StatusConflict:
			refused++
			if errorCode(rr) != "owned_account_limit" {
				t.Errorf("limit error: %s", rr.Body.String())
			}
		default:
			t.Errorf("create: %d %s", rr.Code, rr.Body.String())
		}
	}
	if created != 1 || refused != 1 {
		t.Fatalf("created %d refused %d; want one each", created, refused)
	}
	// Inactive accounts still count. Ordinary accounts and another owner do not.
	if err := db.Model(&models.Mailbox{}).Where("owner_id = ?", fx.admin.ID).Update("active", false).Error; err != nil {
		t.Fatal(err)
	}
	if rr := createQuotaAccount(h, fx.admin.ID, "third@"+fx.domain.Name, ""); rr.Code != 409 {
		t.Fatalf("inactive counts: %d %s", rr.Code, rr.Body.String())
	}
	unlimited := NewAccountHandler(db, grants.NewCache(db, grants.CacheTTL), "")
	if rr := createQuotaAccount(unlimited, fx.admin.ID, "unlimited@"+fx.domain.Name, ""); rr.Code != 201 {
		t.Fatalf("default unlimited: %d %s", rr.Code, rr.Body.String())
	}
}

func TestOwnedQuotaExplicitChildCap(t *testing.T) {
	db := openAccountsTestDB(t)
	fx := seedAccountsFixture(t, db)
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })
	if err := tx.Model(&fx.admin).Update("quota_bytes", 100).Error; err != nil {
		t.Fatal(err)
	}
	child := quotaChild(t, tx, fx.admin, "small", 5, 0)
	if rr := deliverQuotaMessage(tx, child.ID, "123456"); rr.Code != 422 {
		t.Fatalf("child cap: %d %s", rr.Code, rr.Body.String())
	}
	if rr := deliverQuotaMessage(tx, child.ID, "12345"); rr.Code != 201 {
		t.Fatalf("child boundary: %d %s", rr.Code, rr.Body.String())
	}
	if err := tx.Model(&fx.admin).Update("quota_bytes", 0).Error; err != nil {
		t.Fatal(err)
	}
	if rr := deliverQuotaMessage(tx, child.ID, "1"); rr.Code != 422 {
		t.Fatalf("unlimited owner preserves child cap: %d %s", rr.Code, rr.Body.String())
	}
	if err := tx.Model(&child).Update("quota_bytes", 0).Error; err != nil {
		t.Fatal(err)
	}
	if rr := deliverQuotaMessage(tx, child.ID, strings.Repeat("x", 101)); rr.Code != 201 {
		t.Fatalf("unlimited inheritance: %d %s", rr.Code, rr.Body.String())
	}
}
