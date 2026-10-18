package bootstrap

import (
	"fmt"
	"strings"
	"time"

	"github.com/restmail/restmail/internal/audit"
	"github.com/restmail/restmail/internal/db/models"
	"golang.org/x/crypto/ssh"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Serializes first enrollment and operator changes across API replicas.
const ownerRegistryLock int64 = 0x6f776e65726b6579

func ownerKeyRows(spec string) ([]models.OwnerKey, error) {
	keys, err := ParseOwnerKeys(spec)
	if err != nil {
		return nil, err
	}
	rows := make([]models.OwnerKey, 0, keys.Len())
	seen := make(map[string]bool)
	for _, wire := range keys.keys {
		pub, err := ssh.ParsePublicKey(wire)
		if err != nil {
			return nil, err
		}
		fingerprint := ssh.FingerprintSHA256(pub)
		if seen[fingerprint] {
			continue
		}
		seen[fingerprint] = true
		rows = append(rows, models.OwnerKey{
			Fingerprint: fingerprint,
			PublicKey:   strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub))),
		})
	}
	return rows, nil
}

// SeedOwnerKeys imports trusted installation input once. Once any registry row
// exists, PostgreSQL is authoritative: changing an environment variable or
// restarting cannot replace owners or resurrect revoked keys.
func SeedOwnerKeys(database *gorm.DB, spec string) error {
	rows, err := ownerKeyRows(spec)
	if err != nil || len(rows) == 0 {
		return err
	}
	return database.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", ownerRegistryLock).Error; err != nil {
			return err
		}
		var count int64
		if err := tx.Model(&models.OwnerKey{}).Count(&count).Error; err != nil {
			return err
		}
		if count != 0 {
			return nil
		}
		if err := tx.Create(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			if err := auditOwnerKey(tx, "owner_keys.enroll", "installer", row.Fingerprint); err != nil {
				return err
			}
		}
		return nil
	})
}

// LoadOwnerKeys reads the active registry for each signed request. Revocation
// therefore takes effect on every replica without restarting a process.
func LoadOwnerKeys(database *gorm.DB) (*OwnerKeys, error) {
	var rows []models.OwnerKey
	if err := database.Where("revoked_at IS NULL").Order("fingerprint").Find(&rows).Error; err != nil {
		return nil, err
	}
	var lines []string
	for _, row := range rows {
		lines = append(lines, row.PublicKey)
	}
	return ParseOwnerKeys(strings.Join(lines, "\n"))
}

// RegisterOwnerKeys is an explicit trusted operator action, separate from
// first-start seeding. Adding a previously revoked key explicitly restores it.
func RegisterOwnerKeys(database *gorm.DB, spec string) ([]models.OwnerKey, error) {
	rows, err := ownerKeyRows(spec)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("at least one public owner key is required")
	}
	err = database.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", ownerRegistryLock).Error; err != nil {
			return err
		}
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "fingerprint"}},
			DoUpdates: clause.Assignments(map[string]any{"revoked_at": nil}),
		}).Create(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			if err := auditOwnerKey(tx, "owner_keys.register", "operator", row.Fingerprint); err != nil {
				return err
			}
		}
		return nil
	})
	return rows, err
}

// RevokeOwnerKey keeps the row so later bootstrap input cannot re-enroll it.
func RevokeOwnerKey(database *gorm.DB, fingerprint string, now time.Time) error {
	return database.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", ownerRegistryLock).Error; err != nil {
			return err
		}
		result := tx.Model(&models.OwnerKey{}).Where("fingerprint = ?", fingerprint).
			Update("revoked_at", now.UTC())
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return auditOwnerKey(tx, "owner_keys.revoke", "operator", fingerprint)
	})
}

func auditOwnerKey(database *gorm.DB, action, actor, fingerprint string) error {
	return audit.Append(database, &models.AuditEntry{
		ActorKind: models.AuditActorSystem,
		ActorName: actor,
		Action:    action,
		Outcome:   models.AuditOutcomeSuccess,
		Method:    "OPERATOR",
		Route:     "owner-key",
		Status:    200,
		Target:    models.AuditTarget{"fingerprint": fingerprint},
	})
}
