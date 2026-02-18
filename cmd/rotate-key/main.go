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
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintf(os.Stderr, "Usage: %s <old-key> <new-key>\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "\nRe-encrypts all DKIM private keys and certificate private keys\n")
		fmt.Fprintf(os.Stderr, "in the database using the new master key.\n")
		fmt.Fprintf(os.Stderr, "\nEnvironment variables: DB_HOST, DB_PORT, DB_NAME, DB_USER, DB_PASS\n")
		os.Exit(1)
	}
	oldKey := os.Args[1]
	newKey := os.Args[2]

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

	var failures int

	// Re-encrypt DKIM private keys
	var dkimKeys []models.DKIMKey
	if err := database.Find(&dkimKeys).Error; err != nil {
		log.Fatalf("failed to query DKIM keys: %v", err)
	}
	for _, dk := range dkimKeys {
		if dk.PrivateKeyPEM == "" {
			continue
		}
		plain, err := crypto.DecryptString(dk.PrivateKeyPEM, oldKey)
		if err != nil {
			log.Printf("WARN: failed to decrypt DKIM key %d (selector=%s): %v", dk.ID, dk.Selector, err)
			failures++
			continue
		}
		encrypted, err := crypto.EncryptString(plain, newKey)
		if err != nil {
			log.Fatalf("failed to re-encrypt DKIM key %d: %v", dk.ID, err)
		}
		if err := database.Model(&dk).Update("private_key_pem", encrypted).Error; err != nil {
			log.Fatalf("failed to update DKIM key %d: %v", dk.ID, err)
		}
		log.Printf("rotated DKIM key %d (selector=%s)", dk.ID, dk.Selector)
	}

	// Re-encrypt certificate private keys
	var certs []models.Certificate
	if err := database.Find(&certs).Error; err != nil {
		log.Fatalf("failed to query certificates: %v", err)
	}
	for _, cert := range certs {
		if cert.KeyPEM == "" {
			continue
		}
		plain, err := crypto.DecryptString(cert.KeyPEM, oldKey)
		if err != nil {
			log.Printf("WARN: failed to decrypt certificate key %d (issuer=%s): %v", cert.ID, cert.Issuer, err)
			failures++
			continue
		}
		encrypted, err := crypto.EncryptString(plain, newKey)
		if err != nil {
			log.Fatalf("failed to re-encrypt certificate key %d: %v", cert.ID, err)
		}
		if err := database.Model(&cert).Update("key_pem", encrypted).Error; err != nil {
			log.Fatalf("failed to update certificate key %d: %v", cert.ID, err)
		}
		log.Printf("rotated certificate key %d (issuer=%s)", cert.ID, cert.Issuer)
	}

	fmt.Printf("\nMaster key rotation complete. %d DKIM keys, %d certificates processed.\n",
		len(dkimKeys), len(certs))
	if failures > 0 {
		fmt.Printf("WARNING: %d keys could not be decrypted (see warnings above).\n", failures)
		os.Exit(1)
	}
}
