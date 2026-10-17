package filters

import (
	"context"
	"testing"

	"github.com/restmail/restmail/internal/pipeline"
)

// arcEmail builds an inbound message From the given domain, and a context
// carrying the arc_verify verdict with the sealing domain — exactly what
// arc_verify records for a structurally/cryptographically valid chain. It has
// NO SPF/DKIM alignment, so DMARC fails on its own and only an ARC override
// could rescue it.
func arcEmail(fromDomain, arcStatus, arcSealer string) (context.Context, *pipeline.EmailJSON) {
	ctx := authCtx(pipeline.AuthResult{Method: pipeline.AuthARC, Result: arcStatus, Domain: arcSealer})
	return ctx, &pipeline.EmailJSON{
		Headers: pipeline.Headers{
			From: []pipeline.Address{{Address: "ceo@" + fromDomain}},
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

	res, err := f.Execute(arcEmail("victim.example", "pass", "attacker.example"))
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
	res, err := filt.Execute(arcEmail("victim.example", "pass", "Forwarder.Example"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Action != pipeline.ActionContinue || res.Log.Result != "pass" {
		t.Fatalf("trusted-sealer ARC override should rescue DMARC: action=%s result=%s detail=%q",
			res.Action, res.Log.Result, res.Log.Detail)
	}

	// A different (untrusted) sealer on the same filter must NOT override.
	res, err = filt.Execute(arcEmail("victim.example", "pass", "other.example"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Action != pipeline.ActionReject {
		t.Fatalf("non-allowlisted sealer must not override DMARC p=reject: action=%s result=%s detail=%q",
			res.Action, res.Log.Result, res.Log.Detail)
	}
}

// TestARCVerify_RecordsSealerDomain confirms arc_verify records the most recent
// ARC-Seal's d= (sealing domain) with its verdict, which is what dmarc_check
// gates the trusted-sealer override on. A single-set chain (cv=none) is
// structurally valid, so the sealer is recorded.
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

	ctx := authCtx()
	if _, err := f.Execute(ctx, email); err != nil {
		t.Fatal(err)
	}
	got := pipeline.AuthResultsFromContext(ctx).Find(pipeline.AuthARC, "")
	if len(got) != 1 || got[0].Domain != "forwarder.example" {
		t.Fatalf("arc_verify should record the sealer forwarder.example, got %+v", got)
	}
}
