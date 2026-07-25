package filters

// Item 4 (issue #201): the transform filters build their working copy with
// `modified := *email`, a shallow struct copy that SHARES the Headers.Raw /
// Headers.Extra / Metadata maps (and their slice values) with the caller's
// original. A delete/append/overwrite on the copy therefore mutates the
// ORIGINAL too, and dkim_verify's Extra overwrite clobbers spf_check's
// Authentication-Results entry. These tests pin the fixed behaviour: the input
// is never mutated, and the dkim + spf AR entries coexist.

import (
	"context"
	"strings"
	"testing"

	"github.com/restmail/restmail/internal/pipeline"
)

// TestHeaderCleanup_DoesNotMutateOriginal proves header_cleanup removes internal
// headers from its returned copy WITHOUT deleting them from the caller's message.
func TestHeaderCleanup_DoesNotMutateOriginal(t *testing.T) {
	orig := &pipeline.EmailJSON{
		Headers: pipeline.Headers{
			Bcc: []pipeline.Address{{Address: "bcc@example.com"}},
			Raw: map[string][]string{
				"X-Pipeline-ID": {"secret-123"},
				"Subject":       {"hello"},
			},
		},
	}

	f, err := NewHeaderCleanup(nil)
	if err != nil {
		t.Fatalf("NewHeaderCleanup: %v", err)
	}
	res, err := f.Execute(context.Background(), orig)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// The returned (delivered) message must have the internal header removed.
	if _, ok := res.Message.Headers.Raw["X-Pipeline-ID"]; ok {
		t.Error("returned message still carries X-Pipeline-ID (cleanup did nothing)")
	}
	// The ORIGINAL must be untouched.
	if _, ok := orig.Headers.Raw["X-Pipeline-ID"]; !ok {
		t.Error("original message was mutated: X-Pipeline-ID was deleted from the caller's map")
	}
	if len(orig.Headers.Bcc) == 0 {
		t.Error("original message was mutated: Bcc was cleared on the caller's struct")
	}
}

// TestDKIMVerify_DoesNotMutateOriginal proves dkim_verify adds its
// Authentication-Results to its returned copy only, leaving the caller's maps
// untouched.
func TestDKIMVerify_DoesNotMutateOriginal(t *testing.T) {
	orig := &pipeline.EmailJSON{
		Headers: pipeline.Headers{
			Raw:   map[string][]string{},
			Extra: map[string]string{},
		},
	}

	f, _ := NewDKIMVerify(nil)
	if _, err := f.Execute(context.Background(), orig); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if len(orig.Headers.Raw["Authentication-Results"]) != 0 {
		t.Errorf("original Raw AR mutated: %v", orig.Headers.Raw["Authentication-Results"])
	}
	if orig.Headers.Extra["Authentication-Results"] != "" {
		t.Errorf("original Extra AR mutated: %q", orig.Headers.Extra["Authentication-Results"])
	}
}

// TestDKIMVerify_CoexistsWithSPFAuthResults proves that running spf_check then
// dkim_verify leaves BOTH the spf= and dkim= entries in the message — dkim must
// not overwrite the spf Authentication-Results (Raw and Extra).
func TestDKIMVerify_CoexistsWithSPFAuthResults(t *testing.T) {
	email := &pipeline.EmailJSON{
		Envelope: pipeline.Envelope{ClientIP: "203.0.113.7", MailFrom: "user@example.com"},
		Headers: pipeline.Headers{
			From: []pipeline.Address{{Address: "user@example.com"}},
		},
	}

	// spf_check writes an spf= AR entry (in-place on `email`).
	spf, _ := NewSPFCheck(nil)
	spfRes, err := spf.Execute(context.Background(), email)
	if err != nil {
		t.Fatalf("spf Execute: %v", err)
	}
	// spf mutates its input in place; carry it forward as the pipeline would.
	afterSPF := email
	if spfRes.Message != nil {
		afterSPF = spfRes.Message
	}

	dk, _ := NewDKIMVerify(nil)
	dkRes, err := dk.Execute(context.Background(), afterSPF)
	if err != nil {
		t.Fatalf("dkim Execute: %v", err)
	}
	final := dkRes.Message

	raw := strings.Join(final.Headers.Raw["Authentication-Results"], " | ")
	if !strings.Contains(raw, "spf=") {
		t.Errorf("Raw AR lost the spf= entry after dkim_verify: %q", raw)
	}
	if !strings.Contains(raw, "dkim=") {
		t.Errorf("Raw AR missing the dkim= entry: %q", raw)
	}
	extra := final.Headers.Extra["Authentication-Results"]
	if !strings.Contains(extra, "spf=") {
		t.Errorf("Extra AR lost the spf= entry (dkim overwrote it): %q", extra)
	}
	if !strings.Contains(extra, "dkim=") {
		t.Errorf("Extra AR missing the dkim= entry: %q", extra)
	}
}

// TestARCVerify_DoesNotMutateOriginal proves arc_verify writes its AR / metadata
// to the returned copy only.
func TestARCVerify_DoesNotMutateOriginal(t *testing.T) {
	orig := &pipeline.EmailJSON{
		Headers: pipeline.Headers{
			Raw:   map[string][]string{},
			Extra: map[string]string{},
		},
	}

	f, _ := NewARCVerify(nil)
	if _, err := f.Execute(context.Background(), orig); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if len(orig.Headers.Raw["Authentication-Results"]) != 0 {
		t.Errorf("original Raw AR mutated by arc_verify: %v", orig.Headers.Raw["Authentication-Results"])
	}
	if orig.Headers.Extra["Authentication-Results"] != "" {
		t.Errorf("original Extra AR mutated by arc_verify: %q", orig.Headers.Extra["Authentication-Results"])
	}
	if orig.Metadata["arc_status"] != "" {
		t.Errorf("original Metadata mutated by arc_verify: %q", orig.Metadata["arc_status"])
	}
}
