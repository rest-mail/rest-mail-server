package filters

import (
	"context"
	"fmt"
	"strings"

	"github.com/rest-mail/go-dmarc"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/mail"
	"github.com/restmail/restmail/internal/pipeline"
)

// captureDMARC records this message's DMARC evaluation for later RFC 7489
// aggregate (rua) reporting. Only inbound mail whose From domain publishes a
// DMARC record is recorded; the reporter aggregates these rows per period. A DB
// handle is available on the pipeline context; absent one (e.g. the test/preview
// path), capture is a no-op.
//
// Mail with no client IP is not recorded: the SMTP gateway leaves it empty for
// submissions it delivers locally, and an aggregate report row must name the
// IP the mail came from (BuildReport refuses one that doesn't).
//
// override, when set, is why the disposition differs from the requested
// policy (RFC 7489 Appendix C PolicyOverrideReason).
func captureDMARC(ctx context.Context, email *pipeline.EmailJSON, domain, policy string, spfPass, spfAligned, dkimPass, dkimAligned bool, disposition string, override *dmarc.PolicyOverrideReason) {
	db := pipeline.DBFromContext(ctx)
	if db == nil || email.Envelope.Direction == "outbound" || email.Envelope.ClientIP == "" {
		return
	}
	result := func(pass bool) string {
		if pass {
			return "pass"
		}
		return "fail"
	}
	headerFrom := ""
	if len(email.Headers.From) > 0 {
		headerFrom = email.Headers.From[0].Address
	}
	row := models.DMARCAggregateRecord{
		Domain:      domain,
		SourceIP:    email.Envelope.ClientIP,
		HeaderFrom:  headerFrom,
		Disposition: disposition,
		Policy:      policy,
		DKIMResult:  result(dkimPass),
		DKIMAligned: dkimAligned,
		SPFResult:   result(spfPass),
		SPFAligned:  spfAligned,
	}
	if override != nil {
		row.OverrideReason, row.OverrideComment = override.Type, override.Comment
	}
	_ = db.Create(&row).Error
}

// dmarcCheckFilter evaluates DMARC policy using SPF and DKIM results.
type dmarcCheckFilter struct {
	// trustedSealers is the set of ARC sealing domains (lower-cased) whose
	// passing ARC chain is permitted to override a DMARC failure. Empty by
	// default, which keeps ARC purely informational: no sealer is trusted, so an
	// ARC "pass" never rescues a DMARC reject/quarantine (see #178). Bound to the
	// deployment's allowlist in routes.go via NewDMARCCheckWithSealers.
	trustedSealers map[string]struct{}
}

func init() {
	pipeline.DefaultRegistry.Register("dmarc_check", NewDMARCCheck)
}

func NewDMARCCheck(_ []byte) (pipeline.Filter, error) {
	return &dmarcCheckFilter{}, nil
}

// NewDMARCCheckWithSealers returns a factory that binds the trusted-ARC-sealer
// allowlist to the dmarc_check filter. Only a passing ARC chain whose most
// recent ARC-Seal was signed by one of these domains (its d= tag) may override a
// DMARC failure (RFC 8617 makes ARC meaningful only when it comes from a sealer
// you trust). An empty list leaves ARC informational — the secure default.
func NewDMARCCheckWithSealers(sealers []string) pipeline.FilterFactory {
	set := make(map[string]struct{}, len(sealers))
	for _, s := range sealers {
		if s = strings.ToLower(strings.TrimSpace(s)); s != "" {
			set[s] = struct{}{}
		}
	}
	return func(_ []byte) (pipeline.Filter, error) {
		return &dmarcCheckFilter{trustedSealers: set}, nil
	}
}

// sealerTrusted reports whether the given ARC sealing domain is in the
// configured trusted-sealer allowlist. An empty domain or an empty allowlist is
// never trusted, so ARC stays informational by default.
func (f *dmarcCheckFilter) sealerTrusted(sealer string) bool {
	if sealer == "" || len(f.trustedSealers) == 0 {
		return false
	}
	_, ok := f.trustedSealers[strings.ToLower(strings.TrimSpace(sealer))]
	return ok
}

func (f *dmarcCheckFilter) Name() string              { return "dmarc_check" }
func (f *dmarcCheckFilter) Type() pipeline.FilterType { return pipeline.FilterTypeAction }

func (f *dmarcCheckFilter) Execute(ctx context.Context, email *pipeline.EmailJSON) (*pipeline.FilterResult, error) {
	// Get the From domain
	if len(email.Headers.From) == 0 {
		return &pipeline.FilterResult{
			Type:   pipeline.FilterTypeAction,
			Action: pipeline.ActionContinue,
			Log: pipeline.FilterLog{
				Filter: "dmarc_check",
				Result: "none",
				Detail: "no From header",
			},
		}, nil
	}

	fromAddr := email.Headers.From[0].Address
	// A quoted local part can contain @; the domain follows the last one.
	at := strings.LastIndexByte(fromAddr, '@')
	if at <= 0 {
		return &pipeline.FilterResult{
			Type:   pipeline.FilterTypeAction,
			Action: pipeline.ActionContinue,
			Log: pipeline.FilterLog{
				Filter: "dmarc_check",
				Result: "none",
				Detail: "invalid From address",
			},
		}, nil
	}
	// RFC 8616 §6: map U-labels to validated A-labels before discovery and
	// alignment. Preserve the original header, including its local part.
	domain, err := mail.DomainASCII(fromAddr[at+1:])
	if err != nil {
		return &pipeline.FilterResult{
			Type:   pipeline.FilterTypeAction,
			Action: pipeline.ActionContinue,
			Log: pipeline.FilterLog{
				Filter: "dmarc_check",
				Result: "none",
				Detail: "invalid From domain",
			},
		}, nil
	}

	// Discover the From domain's policy: its own record, or else its
	// Organizational Domain's (RFC 7489 §6.6.3). A record with no valid policy
	// and no valid rua= is no policy; one with a rua= but no valid p= requests
	// "none" (§6.6.3 step 6).
	published, err := discoverDMARC(domain)
	if err != nil || published.Raw == "" {
		pipeline.AuthResultsFromContext(ctx).Add(pipeline.AuthResult{
			Method: pipeline.AuthDMARC, Result: "none", Property: pipeline.PropHeaderFrom, Domain: domain,
		})
		return &pipeline.FilterResult{
			Type:   pipeline.FilterTypeAction,
			Action: pipeline.ActionContinue,
			Log: pipeline.FilterLog{
				Filter: "dmarc_check",
				Result: "none",
				Detail: fmt.Sprintf("no DMARC record for %s", domain),
			},
		}, nil
	}

	// The policy for this domain: p=, or sp= (else p=) when the record is its
	// Organizational Domain's.
	policy := published.Requested

	// The SPF, DKIM and ARC verdicts come only from what spf_check, dkim_verify
	// and arc_verify recorded for this run, never from Authentication-Results
	// text: a header can be forged by the sender, a local-part, a record or a
	// script, a recorded verdict cannot.
	auth := pipeline.AuthResultsFromContext(ctx)

	// Alignment is in the record's adkim= and aspf= modes, with Organizational
	// Domains from the Public Suffix List (§3.2), so registrants under a
	// multi-label suffix such as co.uk do not align with each other.
	//
	// SPF: the MAIL FROM identity's result, aligned on the domain it checked
	// (RFC 7489 §3.1.2). A separate HELO result does not count.
	spfPass, spfAligned := false, false
	for _, r := range auth.Find(pipeline.AuthSPF, pipeline.PropMailFrom) {
		if r.Result == "pass" {
			spfPass = true
			if dmarcAligned(r.Domain, domain, published.Record.ASPF) {
				spfAligned = true
			}
		}
	}

	// DKIM: any passing signature whose d= aligns with the From domain. A pass
	// with no signing domain does NOT establish alignment: assuming it would let
	// a signature valid for an unrelated domain satisfy DMARC for this one.
	dkimPass, dkimAligned := false, false
	for _, r := range auth.Find(pipeline.AuthDKIM, pipeline.PropHeaderD) {
		if r.Result == "pass" {
			dkimPass = true
			if dmarcAligned(r.Domain, domain, published.Record.ADKIM) {
				dkimAligned = true
			}
		}
	}

	// ARC override status: a valid ARC chain lets us honor the original
	// authentication on forwarded mail (RFC 8617 §5.2). The verdict and the
	// sealing domain (the d= of the most recent ARC-Seal) are arc_verify's.
	arcStatus, arcSealer := "", ""
	for _, r := range auth.Find(pipeline.AuthARC, "") {
		arcStatus, arcSealer = r.Result, r.Domain
	}

	// An ARC "pass" may override a DMARC failure ONLY when the sealing domain (the
	// d= of the most recent ARC-Seal, recorded by arc_verify) is in the configured
	// trusted-sealer allowlist. RFC 8617 makes ARC meaningful only when it comes
	// from a sealer you trust: without this gate any attacker who runs their own
	// ARC sealer could seal spoofed mail and launder it past the From domain's
	// p=reject/quarantine (#178). The allowlist is empty by default, so ARC stays
	// purely informational unless a sealer is explicitly trusted.
	arcOverride := arcStatus == "pass" && f.sealerTrusted(arcSealer)

	aligned := spfAligned || dkimAligned

	// DMARC's own verdict, for the filters after this one and the stored
	// Authentication-Results. It is DMARC's evaluation alone: a trusted ARC
	// override changes the disposition, not the result (RFC 8617 §7.2).
	verdict := "fail"
	if aligned {
		verdict = "pass"
	}
	auth.Add(pipeline.AuthResult{Method: pipeline.AuthDMARC, Result: verdict, Property: pipeline.PropHeaderFrom, Domain: domain})

	// Disposition actually applied to this message, for aggregate (rua) reports:
	// "none" when DMARC passes or a trusted-sealer ARC override applies, else the
	// published policy.
	disposition := "none"
	if !aligned && !arcOverride && (policy == "reject" || policy == "quarantine") {
		disposition = policy
	}
	var override *dmarc.PolicyOverrideReason
	if !aligned && arcOverride && policy != "none" {
		override = &dmarc.PolicyOverrideReason{Type: dmarc.PolicyOverrideTrustedForwarder, Comment: "arc=pass from " + arcSealer}
	}
	captureDMARC(ctx, email, domain, policy, spfPass, spfAligned, dkimPass, dkimAligned, disposition, override)

	// DMARC requires both pass AND alignment.
	if aligned {
		return &pipeline.FilterResult{
			Type:   pipeline.FilterTypeAction,
			Action: pipeline.ActionContinue,
			Log: pipeline.FilterLog{
				Filter: "dmarc_check",
				Result: "pass",
				Detail: fmt.Sprintf("policy=%s spf_pass=%v spf_aligned=%v dkim_pass=%v dkim_aligned=%v", policy, spfPass, spfAligned, dkimPass, dkimAligned),
			},
		}, nil
	}

	if arcOverride {
		return &pipeline.FilterResult{
			Type:   pipeline.FilterTypeAction,
			Action: pipeline.ActionContinue,
			Log: pipeline.FilterLog{
				Filter: "dmarc_check",
				Result: "pass",
				Detail: fmt.Sprintf("policy=%s dmarc=fail but arc=pass from trusted sealer %s (ARC override) domain=%s", policy, arcSealer, domain),
			},
		}, nil
	}

	// DMARC failed — apply policy
	switch policy {
	case "reject":
		return &pipeline.FilterResult{
			Type:      pipeline.FilterTypeAction,
			Action:    pipeline.ActionReject,
			RejectMsg: fmt.Sprintf("550 DMARC policy reject for %s", domain),
			Log: pipeline.FilterLog{
				Filter: "dmarc_check",
				Result: "fail",
				Detail: fmt.Sprintf("policy=reject domain=%s", domain),
			},
		}, nil
	case "quarantine":
		return &pipeline.FilterResult{
			Type:   pipeline.FilterTypeAction,
			Action: pipeline.ActionQuarantine,
			Log: pipeline.FilterLog{
				Filter: "dmarc_check",
				Result: "fail",
				Detail: fmt.Sprintf("policy=quarantine domain=%s", domain),
			},
		}, nil
	default: // "none" or unknown
		return &pipeline.FilterResult{
			Type:   pipeline.FilterTypeAction,
			Action: pipeline.ActionContinue,
			Log: pipeline.FilterLog{
				Filter: "dmarc_check",
				Result: "fail",
				Detail: fmt.Sprintf("policy=none domain=%s (no enforcement)", domain),
			},
		}, nil
	}
}

// dmarcTXT resolves the _dmarc TXT records DMARC discovery reads; nil uses
// system DNS. A package var so tests can stub DNS.
var dmarcTXT dmarc.TXTResolver

// discoverDMARC finds the DMARC policy that applies to domain.
func discoverDMARC(domain string) (dmarc.Policy, error) {
	return dmarc.Discover(domain, dmarcTXT, mail.OrgDomain)
}

// dmarcAligned applies the same IDNA mapping to both identifiers. In
// particular, an invalid subdomain must not establish relaxed alignment just
// because it has the same Organizational Domain as the From address.
func dmarcAligned(authDomain, fromDomain string, mode dmarc.AlignmentMode) bool {
	authASCII, err := mail.DomainASCII(authDomain)
	if err != nil {
		return false
	}
	fromASCII, err := mail.DomainASCII(fromDomain)
	if err != nil {
		return false
	}
	return dmarc.AlignedMode(authASCII, fromASCII, mode, mail.OrgDomain)
}
