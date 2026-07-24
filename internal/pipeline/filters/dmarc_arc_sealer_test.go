package filters

import (
	"context"
	"testing"

	"github.com/restmail/restmail/internal/pipeline"
)

// arcEmail builds an inbound message From the given domain carrying the local
// arc_verify verdict (arc_status) and the sealing domain (arc_sealer) in
// metadata — exactly what arc_verify records for a structurally/cryptographically
// valid chain. It has NO SPF/DKIM alignment, so DMARC fails on its own and only
// an ARC override could rescue it.
func arcEmail(fromDomain, arcStatus, arcSealer string) *pipeline.EmailJSON {
	return &pipeline.EmailJSON{
		Headers: pipeline.Headers{
			From: []pipeline.Address{{Address: "ceo@" + fromDomain}},
		},
		Metadata: map[string]string{
			"arc_status": arcStatus,
			"arc_sealer": arcSealer,
		},
	}
}

// TestDMARC_UntrustedARCSealerDoesNotOverride is the red-green guard for #178.
// A passing ARC chain sealed by a domain that is NOT in the trusted-sealer
// allowlist must never override DMARC p=reject. With the default (empty)
// allowlist ARC is purely informational, so an attacker who runs their own ARC
// sealer cannot launder a spoofed From past the victim domain's p=reject.
func TestDMARC_UntrustedARCSealerDoesNotOverride(t *testing.T) {
	orig := lookupDMARC
	lookupDMARC = func(string) (string, error) { return "v=DMARC1; p=reject", nil }
	defer func() { lookupDMARC = orig }()

	// Default filter: empty trusted-sealer allowlist.
	f := &dmarcCheckFilter{}

	res, err := f.Execute(context.Background(), arcEmail("victim.example", "pass", "attacker.example"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Action != pipeline.ActionReject {
		t.Fatalf("ARC pass from untrusted sealer bypassed DMARC p=reject: action=%s result=%s detail=%q",
			res.Action, res.Log.Result, res.Log.Detail)
	}
}

// TestDMARC_TrustedARCSealerOverrides confirms the override still works for a
// sealer the operator explicitly trusts: a passing ARC chain sealed by an
// allowlisted domain rescues an otherwise-failing DMARC evaluation (the
// legitimate forwarding case ARC exists for). Matching is case-insensitive.
func TestDMARC_TrustedARCSealerOverrides(t *testing.T) {
	orig := lookupDMARC
	lookupDMARC = func(string) (string, error) { return "v=DMARC1; p=reject", nil }
	defer func() { lookupDMARC = orig }()

	fac := NewDMARCCheckWithSealers([]string{"forwarder.example"})
	filt, err := fac(nil)
	if err != nil {
		t.Fatal(err)
	}

	// Sealer matches the allowlist (case-insensitively) → override applies.
	res, err := filt.Execute(context.Background(), arcEmail("victim.example", "pass", "Forwarder.Example"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Action != pipeline.ActionContinue || res.Log.Result != "pass" {
		t.Fatalf("trusted-sealer ARC override should rescue DMARC: action=%s result=%s detail=%q",
			res.Action, res.Log.Result, res.Log.Detail)
	}

	// A different (untrusted) sealer on the same filter must NOT override.
	res, err = filt.Execute(context.Background(), arcEmail("victim.example", "pass", "other.example"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Action != pipeline.ActionReject {
		t.Fatalf("non-allowlisted sealer must not override DMARC p=reject: action=%s result=%s detail=%q",
			res.Action, res.Log.Result, res.Log.Detail)
	}
}

// TestARCVerify_RecordsSealerDomain confirms arc_verify records the most recent
// ARC-Seal's d= (sealing domain) in metadata as arc_sealer, which is what
// dmarc_check gates the trusted-sealer override on. A single-set chain (cv=none)
// is structurally valid, so the sealer is recorded.
func TestARCVerify_RecordsSealerDomain(t *testing.T) {
	f := &arcVerifyFilter{}
	email := &pipeline.EmailJSON{
		Headers: pipeline.Headers{
			Raw: map[string][]string{
				"Arc-Authentication-Results": {"i=1; restmail; spf=pass"},
				"Arc-Message-Signature":      {"i=1; a=rsa-sha256; d=forwarder.example; s=arc; b=abc"},
				"Arc-Seal":                   {"i=1; a=rsa-sha256; d=forwarder.example; s=arc; cv=none; b=xyz"},
			},
		},
	}

	res, err := f.Execute(context.Background(), email)
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Message.Metadata["arc_sealer"]; got != "forwarder.example" {
		t.Fatalf("arc_verify should record arc_sealer=forwarder.example, got %q (arc_status=%q)",
			got, res.Message.Metadata["arc_status"])
	}
}
