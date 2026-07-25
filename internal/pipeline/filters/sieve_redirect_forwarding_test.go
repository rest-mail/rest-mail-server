package filters

// Redirect forwarding (RFC 5228 §4.2): the sieve filter records `redirect`
// targets in redirect_to for the delivery path to forward, and signals — via
// redirect_suppress_keep — whether a bare redirect cancelled the local copy. It
// also suppresses redirects that would loop. These tests own that filter-side
// contract; the delivery-path consumer that turns redirect_to into outbound mail
// is tested in package handlers.

import (
	"testing"

	"github.com/restmail/restmail/internal/pipeline"
)

// TestSieveRedirect_BareCancelsLocalKeep: a bare `redirect` forwards the message
// AND cancels the implicit keep, so redirect_suppress_keep is set — the delivery
// path forwards without also storing a local copy.
func TestSieveRedirect_BareCancelsLocalKeep(t *testing.T) {
	r := runSieve(t, `redirect "fwd@example.com";`, sieveEmail())

	if r.Action != pipeline.ActionContinue {
		t.Fatalf("Action = %q, want continue", r.Action)
	}
	got := decodeRedirectTargets(t, r.Message.Metadata["redirect_to"])
	if len(got) != 1 || got[0] != "fwd@example.com" {
		t.Fatalf("redirect_to = %v, want [fwd@example.com]", got)
	}
	if r.Message.Metadata["redirect_suppress_keep"] != "true" {
		t.Errorf("bare redirect must cancel the local keep (redirect_suppress_keep=true), got %q",
			r.Message.Metadata["redirect_suppress_keep"])
	}
}

// TestSieveRedirect_CopyPreservesLocalKeep: `redirect :copy` (RFC 3894) forwards
// AND keeps — redirect_suppress_keep is NOT set, so the delivery path also stores
// the local copy.
func TestSieveRedirect_CopyPreservesLocalKeep(t *testing.T) {
	r := runSieve(t, `require ["copy"]; redirect :copy "fwd@example.com";`, sieveEmail())

	if r.Action != pipeline.ActionContinue {
		t.Fatalf("Action = %q, want continue", r.Action)
	}
	got := decodeRedirectTargets(t, r.Message.Metadata["redirect_to"])
	if len(got) != 1 || got[0] != "fwd@example.com" {
		t.Fatalf("redirect_to = %v, want [fwd@example.com]", got)
	}
	if r.Message.Metadata["redirect_suppress_keep"] != "" {
		t.Errorf("redirect :copy must preserve the local keep (no redirect_suppress_keep), got %q",
			r.Message.Metadata["redirect_suppress_keep"])
	}
}

// TestSieveRedirect_FileintoAndRedirectKeepsLocal: a fileinto delivers the
// message locally, so a co-occurring bare redirect forwards but does NOT cancel
// the local copy.
func TestSieveRedirect_FileintoAndRedirectKeepsLocal(t *testing.T) {
	script := `require "fileinto";
fileinto "Archive";
redirect "fwd@example.com";`
	r := runSieve(t, script, sieveEmail())

	if got := r.Message.Metadata["deliver_to_folder"]; got != "Archive" {
		t.Errorf("deliver_to_folder = %q, want Archive", got)
	}
	if got := decodeRedirectTargets(t, r.Message.Metadata["redirect_to"]); len(got) != 1 || got[0] != "fwd@example.com" {
		t.Errorf("redirect_to = %v, want [fwd@example.com]", got)
	}
	if r.Message.Metadata["redirect_suppress_keep"] != "" {
		t.Errorf("fileinto keeps a local copy, so the redirect must not suppress it; got %q",
			r.Message.Metadata["redirect_suppress_keep"])
	}
}

// TestSieveRedirect_MultipleTargets: several redirects each record a target.
func TestSieveRedirect_MultipleTargets(t *testing.T) {
	script := `redirect "a@example.com";
redirect "b@example.com";`
	r := runSieve(t, script, sieveEmail())

	got := decodeRedirectTargets(t, r.Message.Metadata["redirect_to"])
	if len(got) != 2 || got[0] != "a@example.com" || got[1] != "b@example.com" {
		t.Fatalf("redirect_to = %v, want [a@example.com b@example.com]", got)
	}
}

// TestSieveRedirect_LoopSuppressed_DeliveredTo: a redirect to an address already
// in the message's Delivered-To chain is suppressed (no forward), and the message
// is kept locally rather than lost (RFC 5228 §4.2 loop suppression).
func TestSieveRedirect_LoopSuppressed_DeliveredTo(t *testing.T) {
	email := sieveEmail()
	email.Headers.Raw = map[string][]string{"Delivered-To": {"fwd@example.com"}}

	r := runSieve(t, `redirect "fwd@example.com";`, email)

	if got := decodeRedirectTargets(t, r.Message.Metadata["redirect_to"]); len(got) != 0 {
		t.Fatalf("redirect_to = %v, want none (loop suppressed)", got)
	}
	if r.Message.Metadata["redirect_suppress_keep"] == "true" {
		t.Error("a loop-suppressed redirect must not also cancel the local keep (message would be lost)")
	}
	if r.Action != pipeline.ActionContinue {
		t.Errorf("Action = %q, want continue (kept locally)", r.Action)
	}
}

// TestSieveRedirect_LoopSuppressed_Self: a redirect to the message's own
// recipient (a self-redirect) is suppressed.
func TestSieveRedirect_LoopSuppressed_Self(t *testing.T) {
	r := runSieve(t, `redirect "recipient@example.com";`, sieveEmail())

	if got := decodeRedirectTargets(t, r.Message.Metadata["redirect_to"]); len(got) != 0 {
		t.Fatalf("redirect_to = %v, want none (self-redirect suppressed)", got)
	}
	if r.Action != pipeline.ActionContinue {
		t.Errorf("Action = %q, want continue (kept locally)", r.Action)
	}
}
