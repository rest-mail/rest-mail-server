package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"strings"
	"time"

	dkim "github.com/rest-mail/go-dkim"
	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/outbound"
	"golang.org/x/net/idna"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

const dkimEnsureTimeout = 10 * time.Second

var errDKIMMaterial = errors.New("DKIM material is partial or unreadable; explicit operator review is required")

type dkimEnsureResult struct {
	Domain        string `json:"domain"`
	Selector      string `json:"selector"`
	Created       bool   `json:"created"`
	PublicDKIMTXT string `json:"publicDKIMTXT"`
}

func dkimKeyCommand(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "ensure" {
		return fail(stderr, exitUsage, "bad_flag", "dkim-key requires ensure --domain NAME")
	}
	fs := flag.NewFlagSet("dkim-key ensure", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	name := fs.String("domain", "", "an existing mail domain")
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 0 {
		return fail(stderr, exitUsage, "bad_flag", "expected dkim-key ensure --domain NAME")
	}
	// Require the canonical ASCII name already stored by the API. Do not turn
	// an ambiguous operator input into a different database target.
	profile := idna.New(idna.MapForLookup(), idna.BidiRule(), idna.ValidateLabels(true), idna.StrictDomainName(true))
	ascii, err := profile.ToASCII(*name)
	if err != nil || ascii != *name || strings.ToLower(ascii) != ascii || len(ascii) > 253 || !strings.Contains(ascii, ".") || strings.HasSuffix(ascii, ".") {
		return fail(stderr, exitUsage, "bad_flag", "--domain must be an existing canonical ASCII mail domain")
	}
	// Never forward config/driver errors, or debug SQL containing the private
	// value. The connection helper remains connect-only (no schema/RBAC setup).
	database, cfg, status := connectOperatorDatabase(io.Discard)
	if status != exitOK {
		return fail(stderr, exitFailed, "config_or_database", "cannot load deployment configuration or connect to database")
	}
	defer closeDatabase(database)
	if len(cfg.MasterKey) < config.MinMasterKeyLength || strings.TrimSpace(cfg.MasterKey) == "" {
		return fail(stderr, exitFailed, "config", "a valid MASTER_KEY is required")
	}
	result, err := ensureDKIMKey(database, *name, cfg.MasterKey)
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		return fail(stderr, exitFailed, "no_such_domain", "domain does not exist")
	case errors.Is(err, errDKIMMaterial):
		return fail(stderr, exitFailed, "dkim_material", errDKIMMaterial.Error())
	case err != nil:
		return fail(stderr, exitFailed, "dkim_ensure", "cannot ensure DKIM key")
	}
	_ = json.NewEncoder(stdout).Encode(result)
	return exitOK
}

func ensureDKIMKey(database *gorm.DB, name, masterKey string) (dkimEnsureResult, error) {
	var result dkimEnsureResult
	if len(masterKey) < config.MinMasterKeyLength || strings.TrimSpace(masterKey) == "" {
		return result, errDKIMMaterial
	}
	ctx, cancel := context.WithTimeout(context.Background(), dkimEnsureTimeout)
	defer cancel()
	// Silence SQL independently of the caller: an INSERT/UPDATE error can
	// otherwise include ciphertext in GORM's diagnostic query.
	database = database.Session(&gorm.Session{Logger: logger.Discard}).WithContext(ctx)
	err := database.Transaction(func(tx *gorm.DB) error {
		// Server-side bounds also release the row lock if the caller disappears.
		if err := tx.Exec("SET LOCAL lock_timeout = '3s'").Error; err != nil {
			return err
		}
		if err := tx.Exec("SET LOCAL statement_timeout = '8s'").Error; err != nil {
			return err
		}
		var domain models.Domain
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("name = ?", name).Take(&domain).Error; err != nil {
			return err
		}
		keyAbsent, selectorAbsent := domain.DKIMPrivateKey == "", domain.DKIMSelector == ""
		if keyAbsent != selectorAbsent {
			return errDKIMMaterial
		}
		result = dkimEnsureResult{Domain: domain.Name, Selector: domain.DKIMSelector}
		if !keyAbsent {
			plain, err := models.LoadDKIMPrivateKey(domain.DKIMPrivateKey, masterKey)
			if err != nil || outbound.CheckDKIMKey(domain.Name, domain.DKIMSelector, plain) != nil {
				return errDKIMMaterial
			}
			result.PublicDKIMTXT, err = dkimRecordValue(domain.DKIMPrivateKey, masterKey)
			if err != nil {
				return errDKIMMaterial
			}
			return nil
		}
		private, public, err := dkim.GenerateKey(outbound.MinDKIMRSABits)
		if err != nil {
			return err
		}
		result.Selector = dkim.DefaultSelector
		if err := outbound.CheckDKIMKey(domain.Name, result.Selector, private); err != nil {
			return err
		}
		stored, err := models.EncryptDKIMPrivateKey(private, masterKey)
		if err != nil {
			return err
		}
		if !models.DKIMKeyIsEncrypted(stored) {
			return errDKIMMaterial
		}
		result.PublicDKIMTXT, err = dkim.RecordValue(public)
		if err != nil {
			return err
		}
		// NULL and empty strings mean absent in both the locked read and update.
		update := tx.Model(&models.Domain{}).Where("id = ? AND COALESCE(dkim_private_key, '') = '' AND COALESCE(dkim_selector, '') = ''", domain.ID).
			Updates(map[string]any{"dkim_selector": result.Selector, "dkim_private_key": stored, "dkim_created_at": time.Now().UTC()})
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected != 1 {
			return errDKIMMaterial
		}
		result.Created = true
		return nil
	})
	return result, err
}
