package filters

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/rest-mail/go-dmarc"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/mail"
	"github.com/restmail/restmail/internal/nxdomain"
	"github.com/restmail/restmail/internal/pipeline"
)

// dmarcEvaluation is what dmarc_check found for one message, the row its
// aggregate report carries.
type dmarcEvaluation struct {
	domain      string // the From domain
	policy      string // the policy the record requests for it
	disposition string // what was done: none, pass, quarantine or reject
	spf         *pipeline.AuthResult
	spfAligned  bool
	dkim        *pipeline.AuthResult
	dkimAligned bool
	override    *dmarc.PolicyOverrideReason
}

// captureDMARC records this message's DMARC evaluation for later RFC 9990
// aggregate (rua) reporting. Only inbound mail whose From domain publishes a
// DMARC record is recorded; the reporter aggregates these rows per period. A DB
// handle is available on the pipeline context; absent one (e.g. the test/preview
// path), capture is a no-op.
//
// Mail with no client IP is not recorded: the SMTP gateway leaves it empty for
// submissions it delivers locally, and an aggregate report row must name the
// IP the mail came from (BuildReport refuses one that doesn't).
func captureDMARC(ctx context.Context, email *pipeline.EmailJSON, ev dmarcEvaluation) {
	db := pipeline.DBFromContext(ctx)
	if db == nil || email.Envelope.Direction == "outbound" || email.Envelope.ClientIP == "" {
		return
	}
	headerFrom := ""
	if len(email.Headers.From) > 0 {
		headerFrom = email.Headers.From[0].Address
	}
	row := models.DMARCAggregateRecord{
		Domain:      ev.domain,
		SourceIP:    email.Envelope.ClientIP,
		HeaderFrom:  headerFrom,
		Disposition: ev.disposition,
		Policy:      ev.policy,
		DKIMResult:  "none",
		DKIMAligned: ev.dkimAligned,
		SPFResult:   "none",
		SPFAligned:  ev.spfAligned,
	}
	if len(email.Envelope.RcptTo) > 0 {
		row.EnvelopeTo = mail.DomainFromAddress(email.Envelope.RcptTo[0])
	}
	if ev.dkim != nil {
		row.DKIMDomain, row.DKIMSelector, row.DKIMResult = ev.dkim.Domain, ev.dkim.Selector, ev.dkim.Result
	}
	if ev.spf != nil {
		row.SPFDomain, row.SPFResult = ev.spf.Domain, ev.spf.Result
	}
	if ev.override != nil {
		row.OverrideReason, row.OverrideComment = ev.override.Type, ev.override.Comment
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

	// Discover the From domain's policy (RFC 9989 §4.10.1): its own record,
	// or else, by the DNS Tree Walk, its Organizational Domain's or its Public
	// Suffix Domain's. A record with no valid policy and no valid rua= is no
	// policy; one with a rua= but no valid p= requests "none".
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

	// The policy the record requests for this domain: p=, or sp= or np= when
	// the record is its Organizational Domain's or PSD's; and the policy to
	// apply, one level lower when the record asks for test mode (t=y).
	requested, applied := published.Requested, published.Applied

	// The SPF, DKIM and ARC verdicts come only from what spf_check, dkim_verify
	// and arc_verify recorded for this run, never from Authentication-Results
	// text: a header can be forged by the sender, a local-part, a record or a
	// script, a recorded verdict cannot.
	auth := pipeline.AuthResultsFromContext(ctx)

	// Alignment is in the record's adkim= and aspf= modes, with Organizational
	// Domains from the DNS Tree Walk (RFC 9989 §4.10.2), so registrants under a
	// public suffix such as co.uk do not align with each other. A DNS failure
	// on the way leaves the message neither passing nor failing (§5.3.6).
	var dnsErr error
	aligns := func(authDomain string, mode dmarc.AlignmentMode) bool {
		ok, err := dmarcAligned(authDomain, domain, mode)
		if err != nil {
			dnsErr = err
		}
		return ok
	}

	// SPF: the MAIL FROM identity's result, aligned on the domain it checked
	// (RFC 9989 §4.4.2). A separate HELO result does not count.
	var spfResult *pipeline.AuthResult
	spfPass, spfAligned := false, false
	for _, r := range auth.Find(pipeline.AuthSPF, pipeline.PropMailFrom) {
		r := r
		spfResult = &r
		if r.Result == "pass" {
			spfPass = true
			if aligns(r.Domain, published.Record.ASPF) {
				spfAligned = true
			}
		}
	}

	// DKIM: any passing signature whose d= aligns with the From domain. A pass
	// with no signing domain does NOT establish alignment: assuming it would let
	// a signature valid for an unrelated domain satisfy DMARC for this one. The
	// report names the aligned passing signature, else a passing one, else the
	// first.
	var dkimResult *pipeline.AuthResult
	dkimRank := -1
	dkimPass, dkimAligned := false, false
	for _, r := range auth.Find(pipeline.AuthDKIM, pipeline.PropHeaderD) {
		r := r
		rank := 0
		if r.Result == "pass" {
			dkimPass, rank = true, 1
			if aligns(r.Domain, published.Record.ADKIM) {
				dkimAligned, rank = true, 2
			}
		}
		if rank > dkimRank {
			dkimResult, dkimRank = &r, rank
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

	// A DNS failure that kept an identifier from being judged aligned means
	// the message cannot be said to pass or fail, so its policy is not applied
	// (RFC 9989 §5.3.6).
	if !aligned && dnsErr != nil {
		auth.Add(pipeline.AuthResult{Method: pipeline.AuthDMARC, Result: "temperror", Property: pipeline.PropHeaderFrom, Domain: domain})
		return &pipeline.FilterResult{
			Type:   pipeline.FilterTypeAction,
			Action: pipeline.ActionContinue,
			Log: pipeline.FilterLog{
				Filter: "dmarc_check",
				Result: "temperror",
				Detail: fmt.Sprintf("alignment for %s not known: %v", domain, dnsErr),
			},
		}, nil
	}

	// DMARC's own verdict, for the filters after this one and the stored
	// Authentication-Results. It is DMARC's evaluation alone: a trusted ARC
	// override changes the disposition, not the result (RFC 8617 §7.2).
	verdict := "fail"
	if aligned {
		verdict = "pass"
	}
	auth.Add(pipeline.AuthResult{Method: pipeline.AuthDMARC, Result: verdict, Property: pipeline.PropHeaderFrom, Domain: domain})

	// What is done with the message, for the result and the aggregate
	// report: pass (or none, under p=none) when DMARC passes; none when a
	// trusted ARC sealer vouches for it; otherwise the policy to apply. RFC
	// 9989 §5.4 and §7.4 say not to reject on p=reject alone: mail that came
	// through a mailing list (it carries List-Id, RFC 2919), whose
	// authentication a list is known to break, is quarantined instead. Each
	// departure from the requested policy is reported with its reason (RFC
	// 9990 §3.1.6).
	disposition := "none"
	var override *dmarc.PolicyOverrideReason
	switch {
	case aligned:
		if requested == "quarantine" || requested == "reject" {
			disposition = "pass"
		}
	case arcOverride:
		if requested != "none" {
			override = &dmarc.PolicyOverrideReason{Type: dmarc.PolicyOverrideTrustedForwarder, Comment: "arc=pass from " + arcSealer}
		}
	default:
		disposition = applied
		if applied != requested {
			override = &dmarc.PolicyOverrideReason{Type: dmarc.PolicyOverridePolicyTestMode, Comment: "t=y: " + requested + " applied as " + applied}
		}
		if applied == "reject" {
			if lists := email.Headers.Raw["List-Id"]; len(lists) > 0 {
				disposition = "quarantine"
				override = &dmarc.PolicyOverrideReason{Type: dmarc.PolicyOverrideMailingList, Comment: "List-Id: " + strings.TrimSpace(lists[0])}
			}
		}
	}
	captureDMARC(ctx, email, dmarcEvaluation{
		domain: domain, policy: requested, disposition: disposition,
		spf: spfResult, spfAligned: spfAligned, dkim: dkimResult, dkimAligned: dkimAligned, override: override,
	})

	// DMARC requires both pass AND alignment.
	if aligned {
		return &pipeline.FilterResult{
			Type:   pipeline.FilterTypeAction,
			Action: pipeline.ActionContinue,
			Log: pipeline.FilterLog{
				Filter: "dmarc_check",
				Result: "pass",
				Detail: fmt.Sprintf("policy=%s spf_pass=%v spf_aligned=%v dkim_pass=%v dkim_aligned=%v", requested, spfPass, spfAligned, dkimPass, dkimAligned),
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
				Detail: fmt.Sprintf("policy=%s dmarc=fail but arc=pass from trusted sealer %s (ARC override) domain=%s", requested, arcSealer, domain),
			},
		}, nil
	}

	// DMARC failed: apply the disposition.
	detail := fmt.Sprintf("policy=%s domain=%s", requested, domain)
	if override != nil {
		detail += fmt.Sprintf(" applied=%s (%s: %s)", disposition, override.Type, override.Comment)
	}
	switch disposition {
	case "reject":
		return &pipeline.FilterResult{
			Type:      pipeline.FilterTypeAction,
			Action:    pipeline.ActionReject,
			RejectMsg: fmt.Sprintf("550 DMARC policy reject for %s", domain),
			Log:       pipeline.FilterLog{Filter: "dmarc_check", Result: "fail", Detail: detail},
		}, nil
	case "quarantine":
		return &pipeline.FilterResult{
			Type:   pipeline.FilterTypeAction,
			Action: pipeline.ActionQuarantine,
			Log:    pipeline.FilterLog{Filter: "dmarc_check", Result: "fail", Detail: detail},
		}, nil
	default: // "none"
		return &pipeline.FilterResult{
			Type:   pipeline.FilterTypeAction,
			Action: pipeline.ActionContinue,
			Log:    pipeline.FilterLog{Filter: "dmarc_check", Result: "fail", Detail: detail + " (no enforcement)"},
		}, nil
	}
}

// dmarcTXT resolves the _dmarc TXT records DMARC discovery and the DNS Tree
// Walks of alignment read; nil uses system DNS. A package var so tests can
// stub DNS.
var dmarcTXT dmarc.TXTResolver

// dmarcExists says whether a domain exists, for np= (RFC 9989 §3.2.13): not
// when the system's DNS servers answer NXDOMAIN (RFC 8020). A package var so
// tests can stub DNS.
var dmarcExists dmarc.ExistsFunc = func(domain string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return nxdomain.System().Exists(ctx, domain)
}

// discoverDMARC finds the DMARC policy that applies to domain.
func discoverDMARC(domain string) (dmarc.Policy, error) {
	return dmarc.Discover(domain, dmarcTXT, dmarcExists)
}

// dmarcAligned applies the same IDNA mapping to both identifiers. In
// particular, an invalid subdomain must not establish relaxed alignment just
// because it has the same Organizational Domain as the From address. A DNS
// failure in a Tree Walk is returned.
func dmarcAligned(authDomain, fromDomain string, mode dmarc.AlignmentMode) (bool, error) {
	authASCII, err := mail.DomainASCII(authDomain)
	if err != nil {
		return false, nil
	}
	fromASCII, err := mail.DomainASCII(fromDomain)
	if err != nil {
		return false, nil
	}
	return dmarc.AlignedMode(authASCII, fromASCII, mode, dmarcTXT)
}
