// Command rotate-key re-encrypts every at-rest secret in the database under a new
// master key. It MUST cover the complete set of encrypted columns the running
// server actually reads, or a "successful" rotation silently breaks signing/login:
//
//   - domains.dkim_private_key   — the live DKIM signing keys (dkim:v1: form)
//   - credentials.encrypted_secret — TOTP secrets
//   - certificates.key_pem        — ACME/TLS private keys
//   - acme_accounts.key_pem       — the API's ACME account keys
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
	"github.com/restmail/restmail/internal/outbound"
	"gorm.io/gorm"
)

func usage() {
	fmt.Fprintf(os.Stderr, "Usage: ROTATE_OLD_MASTER_KEY=… ROTATE_NEW_MASTER_KEY=… %s\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "\nRe-encrypts all at-rest secrets (DKIM signing keys, TOTP 2FA secrets,\n")
	fmt.Fprintf(os.Stderr, "TLS/ACME private keys and ACME account keys) in the database under the\n")
	fmt.Fprintf(os.Stderr, "new master key.\n")
	fmt.Fprintf(os.Stderr, "\nThe keys are read only from the environment, never from arguments, which\n")
	fmt.Fprintf(os.Stderr, "ps(1) output and shell history would show.\n")
	fmt.Fprintf(os.Stderr, "\nDatabase environment variables: DB_HOST, DB_PORT, DB_NAME, DB_USER, DB_PASS\n")
}

// readKeys reads the old and new master keys from the environment
// (ROTATE_OLD_MASTER_KEY / ROTATE_NEW_MASTER_KEY), so the secrets are never
// exposed in ps(1) output or shell history.
func readKeys() (oldKey, newKey string, ok bool) {
	oldKey = os.Getenv("ROTATE_OLD_MASTER_KEY")
	newKey = os.Getenv("ROTATE_NEW_MASTER_KEY")
	return oldKey, newKey, oldKey != "" && newKey != "" && len(os.Args) == 1
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
		"%d certificates, %d ACME accounts processed.\n",
		summary.DomainDKIM, summary.TwoFactor, summary.Certificates, summary.ACMEAccounts)
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
	ACMEAccounts int
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

	if s.ACMEAccounts, failures, err = rotateACMEAccounts(database, oldKey, newKey); err != nil {
		return s, err
	}
	s.Failures += failures

	return s, nil
}

// rotateDomainDKIM re-encrypts domains.dkim_private_key — the live DKIM signing
// keys read by the signing path. It decrypts the dkim:v1: form with the old key
// and re-encrypts with the new key, preserving the versioned at-rest form.
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
		// A key stored before the API checked keys (#391) may be one the API
		// would now refuse. It is rotated all the same: dropping it, or leaving
		// it under the old key, would stop the domain's mail at once. Say so,
		// so it is replaced.
		if cerr := outbound.CheckDKIMKey(d.Name, d.DKIMSelector, plain); cerr != nil {
			log.Printf("WARN: DKIM key for domain %s (id=%d) would be refused if set today, replace it: %v", d.Name, d.ID, cerr)
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

// rotateTwoFactorSecrets re-encrypts credentials.encrypted_secret — the TOTP
// credentials' secrets (base64 AES-256-GCM ciphertext, no prefix), revoked
// ones too, so none is left under the old key.
func rotateTwoFactorSecrets(database *gorm.DB, oldKey, newKey string) (processed, failures int, err error) {
	var tfs []models.Credential
	if err := database.Where("kind = ?", models.CredentialKindTOTP).Find(&tfs).Error; err != nil {
		return 0, 0, fmt.Errorf("query credentials: %w", err)
	}
	for i := range tfs {
		tf := &tfs[i]
		if tf.EncryptedSecret == "" {
			continue
		}
		plain, derr := crypto.DecryptString(tf.EncryptedSecret, oldKey)
		if derr != nil {
			log.Printf("WARN: failed to decrypt 2FA secret (credential id=%d, account %d): %v", tf.ID, tf.AccountID, derr)
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
		log.Printf("rotated 2FA secret (credential id=%d, account %d)", tf.ID, tf.AccountID)
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

// rotateACMEAccounts re-encrypts acme_accounts.key_pem, the API's account key
// per CA directory. One left under the old key stops every ACME order.
func rotateACMEAccounts(database *gorm.DB, oldKey, newKey string) (processed, failures int, err error) {
	var accounts []models.ACMEAccount
	if err := database.Find(&accounts).Error; err != nil {
		return 0, 0, fmt.Errorf("query ACME accounts: %w", err)
	}
	for i := range accounts {
		account := &accounts[i]
		plain, derr := crypto.DecryptString(account.KeyPEM, oldKey)
		if derr != nil {
			log.Printf("WARN: failed to decrypt ACME account key (directory=%s): %v", account.Directory, derr)
			failures++
			continue
		}
		enc, eerr := crypto.EncryptString(plain, newKey)
		if eerr != nil {
			return processed, failures, fmt.Errorf("re-encrypt ACME account key for %s: %w", account.Directory, eerr)
		}
		if uerr := database.Model(account).Update("key_pem", enc).Error; uerr != nil {
			return processed, failures, fmt.Errorf("update ACME account key for %s: %w", account.Directory, uerr)
		}
		processed++
		log.Printf("rotated ACME account key (directory=%s)", account.Directory)
	}
	return processed, failures, nil
}
