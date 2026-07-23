package handlers

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

// linkFixture seeds a single "target" mailbox that callers race to link, plus n
// distinct webmail accounts (each with its own primary mailbox) that will attempt
// the link. Everything is torn down at the end so the shared unit-test database is
// left clean. A unique domain per run keeps parallel/prior runs from colliding.
type linkFixture struct {
	domain   models.Domain
	target   models.Mailbox
	accounts []models.WebmailAccount
	password string
}

func seedLinkFixture(t *testing.T, db *gorm.DB, n int) linkFixture {
	t.Helper()

	const password = "s3cret-pw"
	hashed, err := auth.HashPassword(password)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}

	domain := models.Domain{Name: fmt.Sprintf("link-%d.test", time.Now().UnixNano())}
	if err := db.Create(&domain).Error; err != nil {
		t.Fatalf("create domain: %v", err)
	}

	target := models.Mailbox{
		DomainID:  domain.ID,
		LocalPart: "target",
		Address:   fmt.Sprintf("target@%s", domain.Name),
		Password:  hashed,
		Active:    true,
	}
	if err := db.Create(&target).Error; err != nil {
		t.Fatalf("create target mailbox: %v", err)
	}

	accounts := make([]models.WebmailAccount, n)
	for i := 0; i < n; i++ {
		pm := models.Mailbox{
			DomainID:  domain.ID,
			LocalPart: fmt.Sprintf("user%d", i),
			Address:   fmt.Sprintf("user%d@%s", i, domain.Name),
			Password:  hashed,
			Active:    true,
		}
		if err := db.Create(&pm).Error; err != nil {
			t.Fatalf("create primary mailbox %d: %v", i, err)
		}
		acct := models.WebmailAccount{PrimaryMailboxID: pm.ID}
		if err := db.Create(&acct).Error; err != nil {
			t.Fatalf("create webmail account %d: %v", i, err)
		}
		accounts[i] = acct
	}

	t.Cleanup(func() {
		db.Where("mailbox_id = ?", target.ID).Delete(&models.LinkedAccount{})
		for _, a := range accounts {
			db.Where("webmail_account_id = ?", a.ID).Delete(&models.LinkedAccount{})
			db.Delete(&models.WebmailAccount{}, a.ID)
		}
		db.Where("domain_id = ?", domain.ID).Delete(&models.Mailbox{})
		db.Delete(&models.Domain{}, domain.ID)
	})

	return linkFixture{domain: domain, target: target, accounts: accounts, password: password}
}

func (fx linkFixture) linkBody() string {
	return fmt.Sprintf(`{"address":"target@%s","password":%q}`, fx.domain.Name, fx.password)
}

// TestLinkAccount_ConcurrentTOCTOU fires many concurrent link requests, each from a
// different webmail account, all targeting the same mailbox. Exactly one must win
// (201) and the rest must conflict (409); the database must end with exactly one
// linked_accounts row for the mailbox. Without the transaction + unique
// linked_accounts(mailbox_id) index (OSI-21), two racers could both pass the
// existence check and both insert, double-linking one mailbox to two accounts.
func TestLinkAccount_ConcurrentTOCTOU(t *testing.T) {
	db := openAuthzTestDB(t)
	const n = 8
	fx := seedLinkFixture(t, db, n)

	h := NewAccountHandler(db)
	body := fx.linkBody()

	var wg sync.WaitGroup
	start := make(chan struct{})
	codes := make([]int, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			rr := httptest.NewRecorder()
			h.LinkAccount(rr, authzRequest(http.MethodPost, "/api/v1/accounts", fx.accounts[i].ID, nil, body))
			codes[i] = rr.Code
		}(i)
	}
	close(start) // release all goroutines together
	wg.Wait()

	created, conflicts := 0, 0
	for _, c := range codes {
		switch c {
		case http.StatusCreated:
			created++
		case http.StatusConflict:
			conflicts++
		default:
			t.Errorf("unexpected status from concurrent link: %d", c)
		}
	}
	if created != 1 {
		t.Fatalf("expected exactly 1 successful link, got created=%d conflicts=%d codes=%v", created, conflicts, codes)
	}

	var count int64
	db.Model(&models.LinkedAccount{}).Where("mailbox_id = ?", fx.target.ID).Count(&count)
	if count != 1 {
		t.Fatalf("expected exactly 1 linked_accounts row for the mailbox, got %d", count)
	}
}

// TestLinkAccount_SingleLinkConstraint is the sequential counterpart: once a mailbox
// is linked to one webmail account, a second, different account cannot link the same
// mailbox — the single-link constraint holds (OSI-21).
func TestLinkAccount_SingleLinkConstraint(t *testing.T) {
	db := openAuthzTestDB(t)
	fx := seedLinkFixture(t, db, 2)

	h := NewAccountHandler(db)
	body := fx.linkBody()

	// First account links the target — succeeds.
	rr := httptest.NewRecorder()
	h.LinkAccount(rr, authzRequest(http.MethodPost, "/api/v1/accounts", fx.accounts[0].ID, nil, body))
	if rr.Code != http.StatusCreated {
		t.Fatalf("first link: expected 201, got %d (%s)", rr.Code, rr.Body.String())
	}

	// Second, different account tries to link the same mailbox — must conflict.
	rr = httptest.NewRecorder()
	h.LinkAccount(rr, authzRequest(http.MethodPost, "/api/v1/accounts", fx.accounts[1].ID, nil, body))
	if rr.Code != http.StatusConflict {
		t.Fatalf("second link (different account): expected 409, got %d (%s)", rr.Code, rr.Body.String())
	}

	var count int64
	db.Model(&models.LinkedAccount{}).Where("mailbox_id = ?", fx.target.ID).Count(&count)
	if count != 1 {
		t.Fatalf("expected exactly 1 linked_accounts row for the mailbox, got %d", count)
	}
}
