package main

import (
	"encoding/json"
	"errors"
	"flag"
	"io"
	"strings"
	"time"

	"github.com/restmail/restmail/internal/bootstrap"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

// Registry maintenance is a trusted local operator action. Normal recovery is
// still a signed HTTPS request and never invokes these commands.
func ownerKeyCommand(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 || (args[0] != "add" && args[0] != "list" && args[0] != "revoke") {
		return fail(stderr, exitUsage, "bad_flag", "owner-key requires add, list or revoke")
	}
	action := args[0]
	fs := flag.NewFlagSet("owner-key", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fingerprint := fs.String("fingerprint", "", "the public key's SHA256 fingerprint")
	if err := fs.Parse(args[1:]); err != nil {
		return fail(stderr, exitUsage, "bad_flag", err.Error())
	}
	if fs.NArg() != 0 || (action == "revoke" && *fingerprint == "") || (action != "revoke" && *fingerprint != "") {
		return fail(stderr, exitUsage, "bad_flag", "only revoke takes --fingerprint; no positional arguments")
	}
	var spec string
	if action == "add" {
		const maxPublicKeys = 64 * 1024
		raw, err := io.ReadAll(io.LimitReader(stdin, maxPublicKeys+1))
		if err != nil || len(raw) > maxPublicKeys {
			return fail(stderr, exitUsage, "bad_key", "public key input must be at most 64 KiB")
		}
		spec = string(raw)
		keys, err := bootstrap.ParseOwnerKeys(spec)
		if err != nil || keys.Len() == 0 {
			return fail(stderr, exitUsage, "bad_key", "stdin must contain public SSH keys in authorized_keys format")
		}
	}
	database, _, status := openDatabase(stderr)
	if status != exitOK {
		return status
	}
	defer closeDatabase(database)
	switch action {
	case "add":
		rows, err := bootstrap.RegisterOwnerKeys(database, spec)
		if err != nil {
			return fail(stderr, exitFailed, "owner_keys", err.Error())
		}
		fingerprints := make([]string, 0, len(rows))
		for _, key := range rows {
			fingerprints = append(fingerprints, key.Fingerprint)
		}
		_ = json.NewEncoder(stdout).Encode(map[string]any{"fingerprints": fingerprints})
	case "list":
		keys := make([]models.OwnerKey, 0)
		if err := database.Order("created_at, fingerprint").Find(&keys).Error; err != nil {
			return fail(stderr, exitFailed, "owner_keys", err.Error())
		}
		_ = json.NewEncoder(stdout).Encode(map[string]any{"keys": keys})
	case "revoke":
		err := bootstrap.RevokeOwnerKey(database, strings.TrimSpace(*fingerprint), time.Now())
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fail(stderr, exitUsage, "unknown_key", "no owner key has that fingerprint")
		}
		if err != nil {
			return fail(stderr, exitFailed, "owner_keys", err.Error())
		}
		_ = json.NewEncoder(stdout).Encode(map[string]any{"fingerprint": *fingerprint, "revoked": true})
	}
	return exitOK
}
