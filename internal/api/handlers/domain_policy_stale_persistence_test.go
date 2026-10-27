package handlers

import (
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

func TestDomainPolicyPostgresZeroRowUpdatesNeverUpsert(t *testing.T) {
	if os.Getenv("DB_HOST") == "" {
		t.Skip("PostgreSQL domain-policy zero-row proof runs in CI with DB_HOST; no local database is started")
	}
	db, err := connectTestDB()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.Domain{}, &models.MTASTSPolicy{}, &models.DomainRelay{}); err != nil {
		t.Fatal(err)
	}
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { _ = tx.Rollback().Error })
	domain := models.Domain{Name: fmt.Sprintf("policy-stale-%d.test", time.Now().UnixNano()), Active: true}
	if err := tx.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	policy := models.MTASTSPolicy{DomainID: domain.ID, Mode: "enforce", MXHosts: "old.example.test", MaxAge: 86400, Active: true}
	if err := tx.Create(&policy).Error; err != nil {
		t.Fatal(err)
	}
	relay := models.DomainRelay{DomainID: domain.ID, Transport: "relay", Host: "smtp.example.test", Port: 465, TLSMode: "implicit", Username: "operator", PasswordEnc: "private-ciphertext"}
	if err := tx.Create(&relay).Error; err != nil {
		t.Fatal(err)
	}
	for _, index := range []int{9, 12} {
		t.Run(domainPolicyFaultRoutes[index].name, func(t *testing.T) {
			table := "mtasts_policies"
			suffix := "mta-sts"
			if index == 12 {
				table = "domain_relays"
				suffix = "relay"
			}
			const callback = "domain_policy_zero_row_update"
			if err := tx.Callback().Update().Before("gorm:update").Register(callback, func(q *gorm.DB) {
				if q.Statement.Table == table {
					q.Where("1 = 0")
				}
			}); err != nil {
				t.Fatal(err)
			}
			remove := func() { _ = tx.Callback().Update().Remove(callback) }
			defer remove()
			route := domainPolicyFaultRoutes[index]
			r := httptest.NewRequest(route.method, fmt.Sprintf("/api/v1/admin/domains/%d/%s", domain.ID, suffix), strings.NewReader(route.body))
			r.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			domainPolicyFaultRouter(tx, nil).ServeHTTP(rr, r)
			remove()
			if rr.Code != 500 || strings.Contains(rr.Body.String(), "private") {
				t.Fatalf("zero-row update: %d %s", rr.Code, rr.Body)
			}
			var count int64
			if err := tx.Table(table).Where("domain_id = ?", domain.ID).Count(&count).Error; err != nil || count != 1 {
				t.Fatalf("row count changed: count=%d error=%v", count, err)
			}
			if index == 9 {
				var stored models.MTASTSPolicy
				if err := tx.First(&stored, policy.ID).Error; err != nil {
					t.Fatal(err)
				}
				if stored.Mode != policy.Mode || stored.MXHosts != policy.MXHosts || stored.MaxAge != policy.MaxAge || stored.Active != policy.Active {
					t.Fatal("zero-row update modified policy through fallback")
				}
			} else {
				var stored models.DomainRelay
				if err := tx.First(&stored, relay.ID).Error; err != nil {
					t.Fatal(err)
				}
				if stored.Transport != relay.Transport || stored.Host != relay.Host || stored.PasswordEnc != relay.PasswordEnc {
					t.Fatal("zero-row update modified relay through fallback")
				}
			}
		})
	}
}
