package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"strings"
	"testing"

	"github.com/restmail/restmail/internal/bootstrap"
	"golang.org/x/crypto/ssh"
)

func TestOwnerKeyOperator_RegistrationAndRevocation(t *testing.T) {
	db := scratchDatabase(t)
	// Uses the actual command dispatcher and installs its JSON-only logging.
	if code, out, errOut := run("owner-key", "list"); code != exitOK || out != "{\"keys\":[]}\n" || errOut != "" {
		t.Fatalf("empty registry: %d %q %s", code, out, errOut)
	}
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := ownerKeyCommand([]string{"add"}, bytes.NewReader(ssh.MarshalAuthorizedKey(key)), &out, &errOut); code != exitOK || errOut.Len() != 0 {
		t.Fatalf("register: %d %s", code, errOut.String())
	}
	var result struct {
		Fingerprints []string `json:"fingerprints"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil || len(result.Fingerprints) != 1 || result.Fingerprints[0] != ssh.FingerprintSHA256(key) {
		t.Fatalf("registration output: %s err=%v", out.String(), err)
	}
	keys, err := bootstrap.LoadOwnerKeys(db)
	if err != nil || keys.Len() != 1 {
		t.Fatalf("registry count=%v err=%v", keys, err)
	}
	if code, _, errOut := run("owner-key", "revoke", "--fingerprint", result.Fingerprints[0]); code != exitOK || errOut != "" {
		t.Fatalf("revoke: %d %s", code, errOut)
	}
	keys, err = bootstrap.LoadOwnerKeys(db)
	if err != nil || keys.Len() != 0 {
		t.Fatalf("revoked key still active: %v %v", keys, err)
	}
	if code, out, errOut := run("owner-key", "list"); code != exitOK || !strings.Contains(out, "revoked_at") || errOut != "" {
		t.Fatalf("revoked row missing from registry: %d %s %s", code, out, errOut)
	}
}

func TestOwnerKeyOperator_RefusesPrivateOrInvalidInput(t *testing.T) {
	for _, input := range []string{"", "-----BEGIN OPENSSH PRIVATE KEY-----\nprivate", "ssh-ed25519 invalid"} {
		var out, errOut bytes.Buffer
		if code := ownerKeyCommand([]string{"add"}, strings.NewReader(input), &out, &errOut); code != exitUsage || out.Len() != 0 || errorCodeIn(t, errOut.String()) != "bad_key" {
			t.Fatalf("invalid key accepted: %d %s %s", code, out.String(), errOut.String())
		}
	}
}
