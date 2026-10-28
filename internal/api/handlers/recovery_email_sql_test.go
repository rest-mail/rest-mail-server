package handlers

import (
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/bootstrap"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

// A candidate can have been read by two concurrent redeemers. Only the
// conditional spend may decide which succeeds, inside the caller's transaction.
func TestRecoveryEmailConcurrentTokenSpendUsesConditionalUpdate(t *testing.T) {
	for _, won := range []bool{true, false} {
		name := "loser"
		affected := int64(0)
		if won {
			name = "winner"
			affected = 1
		}
		t.Run(name, func(t *testing.T) {
			now := time.Now()
			tokenStep := domainPolicySQLStep{match: `FROM "one_time_tokens"`, columns: []string{"id", "account_id", "created_at", "expires_at"}, values: []driver.Value{int64(4), int64(7), now, now.Add(time.Minute)}}
			d := &domainPolicyFaultDB{steps: []domainPolicySQLStep{
				tokenStep,
				{match: `FOR UPDATE`, columns: []string{"id", "address", "active"}, values: []driver.Value{int64(7), "person@hosted.test", true}},
				{match: `FROM "recovery_emails"`, columns: []string{"account_id", "address", "verified_at"}, values: []driver.Value{int64(7), "backup@external.test", now}},
				{match: `FROM "domains"`, columns: []string{"count"}, values: []driver.Value{int64(0)}},
				{match: `UPDATE "one_time_tokens"`, affected: affected},
			}}
			if won {
				d.steps = append(d.steps, tokenStep)
			}
			db := domainPolicyFaultGorm(t, d)
			err := db.Transaction(func(tx *gorm.DB) error {
				_, _, err := bootstrap.RecoveryEmailTokenAccount(t.Context(), tx, "private-token", models.OneTimeTokenRecoverEmail)
				return err
			})
			if won && err != nil || !won && !errors.Is(err, bootstrap.ErrTokenInvalid) {
				t.Fatalf("winner=%v err=%v", won, err)
			}
			for _, guard := range []string{"token_hash =", "purpose =", "used_at IS NULL", "expires_at >"} {
				if !strings.Contains(d.lastWrite, guard) {
					t.Fatalf("missing atomic guard %q: %s", guard, d.lastWrite)
				}
			}
			if strings.Contains(d.lastWrite, "private-token") {
				t.Fatal("plaintext token interpolated in SQL")
			}
			if won && d.commits.Load() != 1 || !won && d.rollbacks.Load() != 1 {
				t.Fatalf("commit=%d rollback=%d", d.commits.Load(), d.rollbacks.Load())
			}
		})
	}
}

func TestRecoveryEmailTokenPredatingPasswordChangeIsRefused(t *testing.T) {
	now := time.Now()
	d := &domainPolicyFaultDB{steps: []domainPolicySQLStep{
		{match: `FROM "one_time_tokens"`, columns: []string{"id", "account_id", "created_at"}, values: []driver.Value{int64(4), int64(7), now.Add(-time.Minute)}},
		{match: `FOR UPDATE`, columns: []string{"id", "address", "active", "last_password_change"}, values: []driver.Value{int64(7), "person@hosted.test", true, now}},
	}}
	db := domainPolicyFaultGorm(t, d)
	err := db.Transaction(func(tx *gorm.DB) error {
		_, _, err := bootstrap.RecoveryEmailTokenAccount(t.Context(), tx, "private-token", models.OneTimeTokenRecoverEmail)
		return err
	})
	if !errors.Is(err, bootstrap.ErrTokenInvalid) || d.calls.Load() != 2 || d.commits.Load() != 0 || d.lastWrite != "" {
		t.Fatalf("stale token changed state: %v %+v", err, d)
	}
}
