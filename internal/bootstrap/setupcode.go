package bootstrap

import (
	"crypto/rand"
	"strings"
	"time"

	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

// The setup code is the secret a fresh install generates for its owner. While
// the server has no admin, every start mints one and prints it to the server's
// own output, as a QR code for the app and as text to type. Every platform can
// show a process's output to whoever runs it (docker logs, kubectl logs,
// journalctl, a terminal), so reading it needs no particular one — and no
// network can.
//
// A start replaces the code of the start before: a code that leaked, or
// scrolled out of the logs, is fixed by a restart. Claiming the server spends
// it, and once there is an admin no code is minted or printed again.

// SetupCodeLifetime bounds how long a printed code works if the server is never
// restarted. A restart prints a fresh one.
const SetupCodeLifetime = 7 * 24 * time.Hour

// Crockford's base32: no I, L, O or U, so nothing reads as something else.
const setupCodeAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// setupCodeLength is 24 characters of 5 bits: 120 bits, as six groups of four.
const setupCodeLength = 24

// EnsureSetupCode mints the code for a server with no admin, replacing any code
// an earlier start printed. It returns "" when the server is already claimed.
func EnsureSetupCode(database *gorm.DB, now time.Time) (string, time.Time, error) {
	code, err := newSetupCode()
	if err != nil {
		return "", time.Time{}, err
	}
	expires := now.Add(SetupCodeLifetime)
	err = database.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("purpose = ? AND used_at IS NULL", models.OneTimeTokenSetupCode).
			Delete(&models.OneTimeToken{}).Error; err != nil {
			return err
		}
		var admins int64
		if err := tx.Model(&models.AdminUser{}).Count(&admins).Error; err != nil {
			return err
		}
		if admins > 0 {
			code = ""
			return nil
		}
		return tx.Create(&models.OneTimeToken{
			Purpose:   models.OneTimeTokenSetupCode,
			TokenHash: hashToken(NormalizeSetupCode(code)),
			ExpiresAt: expires,
		}).Error
	})
	if err != nil || code == "" {
		return "", time.Time{}, err
	}
	return code, expires, nil
}

func newSetupCode() (string, error) {
	raw := make([]byte, setupCodeLength)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	var b strings.Builder
	for i, r := range raw {
		if i > 0 && i%4 == 0 {
			b.WriteByte('-')
		}
		b.WriteByte(setupCodeAlphabet[r&31]) // 256 is a multiple of 32: unbiased
	}
	return b.String(), nil
}

// NormalizeSetupCode makes a code as typed match the code as printed: case,
// dashes and spaces do not matter, and O, I and L read as 0, 1 and 1.
func NormalizeSetupCode(code string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(code) {
		switch r {
		case '-', ' ', '\t':
			continue
		case 'O':
			r = '0'
		case 'I', 'L':
			r = '1'
		}
		b.WriteRune(r)
	}
	return b.String()
}
