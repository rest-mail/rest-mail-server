// Command rotate-key re-encrypts every at-rest secret in the database under a new
// master key. It MUST cover the complete set of encrypted columns the running
// server actually reads, or a "successful" rotation silently breaks signing/login:
//
//   - domains.dkim_private_key   — the live DKIM/ARC signing keys (dkim:v1: form)
//   - two_factor.encrypted_secret — enrolled TOTP secrets
//   - certificates.key_pem        — ACME/TLS private keys
//   - dkim_keys.private_key_pem   — legacy table kept only for migration; no
//     runtime path reads it, but it is rotated too so a partially-migrated
//     database is not left with a mix of old- and new-key ciphertext.
package main

import (
	"fmt"
	"log"
	"os"
	"time"

	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/crypto"
	"github.com/restmail/restmail/internal/db"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

func usage() {
	fmt.Fprintf(os.Stderr, "Usage: %s <old-key> <new-key>\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "   or: ROTATE_OLD_MASTER_KEY=… ROTATE_NEW_MASTER_KEY=… %s\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "\nRe-encrypts all at-rest secrets (DKIM signing keys, TOTP 2FA secrets,\n")
	fmt.Fprintf(os.Stderr, "and TLS/ACME private keys) in the database under the new master key.\n")
	fmt.Fprintf(os.Stderr, "\nPrefer the environment variables: keys passed as arguments are visible in\n")
	fmt.Fprintf(os.Stderr, "ps(1) output and shell history.\n")
	fmt.Fprintf(os.Stderr, "\nDatabase environment variables: DB_HOST, DB_PORT, DB_NAME, DB_USER, DB_PASS\n")
}

// readKeys resolves the old and new master keys, preferring the environment
// (ROTATE_OLD_MASTER_KEY / ROTATE_NEW_MASTER_KEY) over argv so the secrets are
// not exposed in ps(1) output or shell history. argv remains supported for
// backward compatibility.
func readKeys() (oldKey, newKey string, ok bool) {
	oldKey = os.Getenv("ROTATE_OLD_MASTER_KEY")
	newKey = os.Getenv("ROTATE_NEW_MASTER_KEY")
	if oldKey != "" && newKey != "" {
		return oldKey, newKey, true
	}
	if len(os.Args) == 3 {
		fmt.Fprintf(os.Stderr, "warning: master keys passed as command-line arguments are visible in ps(1) and shell history; prefer ROTATE_OLD_MASTER_KEY / ROTATE_NEW_MASTER_KEY\n")
		return os.Args[1], os.Args[2], true
	}
	return "", "", false
}

func main() {
	oldKey, newKey, ok := readKeys()
	if !ok {
		usage()
		os.Exit(1)
	}
	if oldKey == newKey {
		log.Fatal("old and new keys must be different")
	}

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	database, err := db.WaitForDB(cfg, 30*time.Second)
	if err != nil {
		log.Fatalf("failed to connect to database: %v", err)
	}

	summary, err := rotate(database, oldKey, newKey)
	if err != nil {
		log.Fatalf("key rotation failed: %v", err)
	}

	fmt.Printf("\nMaster key rotation complete: %d domain DKIM keys, %d 2FA secrets, "+
		"%d certificates, %d legacy DKIM keys processed.\n",
		summary.DomainDKIM, summary.TwoFactor, summary.Certificates, summary.LegacyDKIM)
	if summary.Failures > 0 {
		fmt.Printf("WARNING: %d secrets could not be decrypted with the old key and were left "+
			"untouched (see warnings above).\n", summary.Failures)
		os.Exit(1)
	}
}

// rotateSummary counts what a rotation touched.
type rotateSummary struct {
	DomainDKIM   int
	TwoFactor    int
	Certificates int
	LegacyDKIM   int
	Failures     int
}

// rotate re-encrypts every at-rest secret from oldKey to newKey. A per-secret
// decrypt failure (a value not encrypted under oldKey) is counted in Failures and
// the row is left byte-for-byte untouched rather than mangled; only an
// infrastructural error (query/re-encrypt/update failure) aborts with an error.
func rotate(database *gorm.DB, oldKey, newKey string) (rotateSummary, error) {
	var s rotateSummary
	var err error
	var failures int

	if s.DomainDKIM, failures, err = rotateDomainDKIM(database, oldKey, newKey); err != nil {
		return s, err
	}
	s.Failures += failures

	if s.TwoFactor, failures, err = rotateTwoFactorSecrets(database, oldKey, newKey); err != nil {
		return s, err
	}
	s.Failures += failures

	if s.Certificates, failures, err = rotateCertificates(database, oldKey, newKey); err != nil {
		return s, err
	}
	s.Failures += failures

	if s.LegacyDKIM, failures, err = rotateLegacyDKIMKeys(database, oldKey, newKey); err != nil {
		return s, err
	}
	s.Failures += failures

	return s, nil
}

// rotateDomainDKIM re-encrypts domains.dkim_private_key — the live DKIM/ARC
// signing keys read by the signing path. It decrypts with the old key (handling
// the dkim:v1: prefix, legacy bare-base64 ciphertext, and plaintext PEM) and
// re-encrypts with the new key, preserving the versioned at-rest form.
func rotateDomainDKIM(database *gorm.DB, oldKey, newKey string) (processed, failures int, err error) {
	var domains []models.Domain
	if err := database.Find(&domains).Error; err != nil {
		return 0, 0, fmt.Errorf("query domains: %w", err)
	}
	for i := range domains {
		d := &domains[i]
		if d.DKIMPrivateKey == "" {
			continue
		}
		plain, derr := models.LoadDKIMPrivateKey(d.DKIMPrivateKey, oldKey)
		if derr != nil {
			log.Printf("WARN: failed to decrypt DKIM private key for domain %s (id=%d): %v", d.Name, d.ID, derr)
			failures++
			continue
		}
		enc, eerr := models.EncryptDKIMPrivateKey(plain, newKey)
		if eerr != nil {
			return processed, failures, fmt.Errorf("re-encrypt DKIM key for domain %s: %w", d.Name, eerr)
		}
		if uerr := database.Model(d).Update("dkim_private_key", enc).Error; uerr != nil {
			return processed, failures, fmt.Errorf("update DKIM key for domain %s: %w", d.Name, uerr)
		}
		processed++
		log.Printf("rotated DKIM private key for domain %s (id=%d)", d.Name, d.ID)
	}
	return processed, failures, nil
}

// rotateTwoFactorSecrets re-encrypts two_factor.encrypted_secret — the enrolled
// TOTP secrets (base64 AES-256-GCM ciphertext, no prefix).
func rotateTwoFactorSecrets(database *gorm.DB, oldKey, newKey string) (processed, failures int, err error) {
	var tfs []models.TwoFactor
	if err := database.Find(&tfs).Error; err != nil {
		return 0, 0, fmt.Errorf("query two_factor: %w", err)
	}
	for i := range tfs {
		tf := &tfs[i]
		if tf.EncryptedSecret == "" {
			continue
		}
		plain, derr := crypto.DecryptString(tf.EncryptedSecret, oldKey)
		if derr != nil {
			log.Printf("WARN: failed to decrypt 2FA secret (id=%d, %s/%d): %v", tf.ID, tf.UserType, tf.SubjectID, derr)
			failures++
			continue
		}
		enc, eerr := crypto.EncryptString(plain, newKey)
		if eerr != nil {
			return processed, failures, fmt.Errorf("re-encrypt 2FA secret id=%d: %w", tf.ID, eerr)
		}
		if uerr := database.Model(tf).Update("encrypted_secret", enc).Error; uerr != nil {
			return processed, failures, fmt.Errorf("update 2FA secret id=%d: %w", tf.ID, uerr)
		}
		processed++
		log.Printf("rotated 2FA secret (id=%d, %s/%d)", tf.ID, tf.UserType, tf.SubjectID)
	}
	return processed, failures, nil
}

// rotateCertificates re-encrypts certificates.key_pem (TLS/ACME private keys).
func rotateCertificates(database *gorm.DB, oldKey, newKey string) (processed, failures int, err error) {
	var certs []models.Certificate
	if err := database.Find(&certs).Error; err != nil {
		return 0, 0, fmt.Errorf("query certificates: %w", err)
	}
	for i := range certs {
		cert := &certs[i]
		if cert.KeyPEM == "" {
			continue
		}
		plain, derr := crypto.DecryptString(cert.KeyPEM, oldKey)
		if derr != nil {
			log.Printf("WARN: failed to decrypt certificate key (id=%d, issuer=%s): %v", cert.ID, cert.Issuer, derr)
			failures++
			continue
		}
		enc, eerr := crypto.EncryptString(plain, newKey)
		if eerr != nil {
			return processed, failures, fmt.Errorf("re-encrypt certificate key id=%d: %w", cert.ID, eerr)
		}
		if uerr := database.Model(cert).Update("key_pem", enc).Error; uerr != nil {
			return processed, failures, fmt.Errorf("update certificate key id=%d: %w", cert.ID, uerr)
		}
		processed++
		log.Printf("rotated certificate key (id=%d, issuer=%s)", cert.ID, cert.Issuer)
	}
	return processed, failures, nil
}

// rotateLegacyDKIMKeys re-encrypts the legacy dkim_keys.private_key_pem column.
// No runtime path reads this table (the live signing keys are on domains), but it
// is rotated so a database that still carries legacy rows is not left with a mix
// of old- and new-key ciphertext.
func rotateLegacyDKIMKeys(database *gorm.DB, oldKey, newKey string) (processed, failures int, err error) {
	var dkimKeys []models.DKIMKey
	if err := database.Find(&dkimKeys).Error; err != nil {
		return 0, 0, fmt.Errorf("query dkim_keys: %w", err)
	}
	for i := range dkimKeys {
		dk := &dkimKeys[i]
		if dk.PrivateKeyPEM == "" {
			continue
		}
		plain, derr := crypto.DecryptString(dk.PrivateKeyPEM, oldKey)
		if derr != nil {
			log.Printf("WARN: failed to decrypt legacy DKIM key (id=%d, selector=%s): %v", dk.ID, dk.Selector, derr)
			failures++
			continue
		}
		enc, eerr := crypto.EncryptString(plain, newKey)
		if eerr != nil {
			return processed, failures, fmt.Errorf("re-encrypt legacy DKIM key id=%d: %w", dk.ID, eerr)
		}
		if uerr := database.Model(dk).Update("private_key_pem", enc).Error; uerr != nil {
			return processed, failures, fmt.Errorf("update legacy DKIM key id=%d: %w", dk.ID, uerr)
		}
		processed++
		log.Printf("rotated legacy DKIM key (id=%d, selector=%s)", dk.ID, dk.Selector)
	}
	return processed, failures, nil
}
