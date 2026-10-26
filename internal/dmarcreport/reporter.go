// Package dmarcreport runs the periodic worker that emits RFC 9990 DMARC
// aggregate (rua) reports for the per-message evaluations captured by the
// dmarc_check filter. Report XML generation and DMARC record parsing live in
// github.com/rest-mail/go-dmarc; this package owns the scheduling, persistence,
// and delivery wiring (reading captured evaluations, finding the rua address,
// and queueing the report email for outbound delivery).
package dmarcreport

import (
	"encoding/base64"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"

	"github.com/rest-mail/go-dmarc/pkg/dmarc"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/version"
	"gorm.io/gorm"
)

// Reporter periodically emits RFC 9990 DMARC aggregate (rua) reports for the
// evaluations captured by the dmarc_check filter.
type Reporter struct {
	db        *gorm.DB
	interval  time.Duration
	orgDomain string
	lookupTXT func(string) ([]string, error)
	stop      chan struct{}
}

// NewReporter creates a reporter that emits aggregate reports every interval
// (typically 24h). orgDomain identifies this reporting organization.
func NewReporter(db *gorm.DB, interval time.Duration, orgDomain string) *Reporter {
	return &Reporter{
		db:        db,
		interval:  interval,
		orgDomain: orgDomain,
		lookupTXT: net.LookupTXT,
		stop:      make(chan struct{}),
	}
}

// Start begins the periodic reporting loop in a background goroutine.
func (r *Reporter) Start() {
	go r.loop()
	slog.Info("dmarc aggregate reporter started", "interval", r.interval, "org", r.orgDomain)
}

// Shutdown stops the reporter.
func (r *Reporter) Shutdown() { close(r.stop) }

func (r *Reporter) loop() {
	timer := time.NewTimer(2 * time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-r.stop:
			return
		case <-timer.C:
			r.run(time.Now())
			timer.Reset(r.interval)
		}
	}
}

func (r *Reporter) run(now time.Time) {
	var domains []string
	if err := r.db.Model(&models.DMARCAggregateRecord{}).
		Where("reported = ?", false).Distinct().Pluck("domain", &domains).Error; err != nil {
		slog.Error("dmarc reporter: list domains", "error", err)
		return
	}
	r.reportDomains(domains, now)
}

// reportDomains reports the unreported evaluations of the given From domains.
// A report covers one DMARC Policy Domain (RFC 9990 §3.1.2): the From domains
// whose policy one record supplies (a domain's own, or its Organizational
// Domain's when it has none) share it, each with records of its own. Policies
// are discovered as dmarc_check discovers them; a DNS failure leaves a
// domain's rows for the next interval.
func (r *Reporter) reportDomains(domains []string, now time.Time) {
	var order []string
	policies := map[string]dmarc.Policy{}
	members := map[string][]string{}
	for _, d := range domains {
		published, err := dmarc.Discover(d, r.lookupTXT, nil)
		if err != nil {
			slog.Warn("dmarc reporter: discovery failed", "domain", d, "error", err)
			continue
		}
		if _, seen := policies[published.Domain]; !seen {
			order = append(order, published.Domain)
			policies[published.Domain] = published
		}
		members[published.Domain] = append(members[published.Domain], d)
	}
	for _, policyDomain := range order {
		if err := r.reportPolicyDomain(policies[policyDomain], members[policyDomain], now); err != nil {
			slog.Warn("dmarc reporter: report failed", "policy_domain", policyDomain, "domains", members[policyDomain], "error", err)
		}
	}
}

// reportPolicyDomain queues the aggregate report of one DMARC Policy Domain
// for the evaluations of its From domains, to every rua= URI it may send to,
// and marks them reported.
func (r *Reporter) reportPolicyDomain(published dmarc.Policy, domains []string, now time.Time) error {
	var records []models.DMARCAggregateRecord
	if err := r.db.Where("domain IN ? AND reported = ?", domains, false).
		Order("created_at ASC").Find(&records).Error; err != nil {
		return err
	}
	if len(records) == 0 {
		return nil
	}

	markReported := func(tx *gorm.DB) error {
		ids := make([]uint, len(records))
		for i, rec := range records {
			ids[i] = rec.ID
		}
		return tx.Model(&models.DMARCAggregateRecord{}).Where("id IN ?", ids).Update("reported", true).Error
	}

	// A record that no longer publishes a policy has nobody to report to.
	if published.Raw == "" {
		return markReported(r.db)
	}

	// A report row must name the IP the mail came from, and BuildReport
	// refuses one that doesn't: such a row is left out rather than holding
	// back the domain's report every interval.
	reportable := make([]models.DMARCAggregateRecord, 0, len(records))
	for _, rec := range records {
		if net.ParseIP(rec.SourceIP) != nil {
			reportable = append(reportable, rec)
		}
	}
	if len(reportable) == 0 {
		return markReported(r.db)
	}

	// RFC 9990 §4: an address outside the policy domain's organization gets
	// reports only if it published that it wants them.
	ruas, err := dmarc.AuthorizeReportURIs(published.Domain, "rua", published.Record.RUA, r.lookupTXT)
	if err != nil {
		return err
	}
	recipients := mailtoRUAs(ruas)
	if len(recipients) == 0 {
		// No rua address we may send this report to: nothing to send; mark
		// reported so the records don't accumulate forever.
		return markReported(r.db)
	}

	begin := records[0].CreatedAt.Unix()
	end := now.Unix()
	reportID := fmt.Sprintf("%d.%s@%s", begin, published.Domain, r.orgDomain)
	meta := dmarc.ReportMetadata{
		OrgName:   r.orgDomain,
		Email:     "dmarc-reports@" + r.orgDomain,
		ReportID:  reportID,
		DateRange: dmarc.DateRange{Begin: begin, End: end},
		Generator: "REST Mail " + version.Version,
	}
	xmlBytes, err := dmarc.BuildReport(meta, dmarc.NewPolicyPublished(published), aggregateRecords(reportable))
	if err != nil {
		return err
	}
	gz, err := dmarc.Gzip(xmlBytes)
	if err != nil {
		return err
	}
	filename, ok := dmarc.ReportFilename(r.orgDomain, published.Domain, meta.DateRange, "", true)
	if !ok {
		return fmt.Errorf("dmarc report filename for %s from %q", published.Domain, r.orgDomain)
	}
	subject, ok := dmarc.ReportSubject(published.Domain, r.orgDomain, reportID)
	if !ok {
		return fmt.Errorf("dmarc report subject for %s from %q", published.Domain, r.orgDomain)
	}

	// RFC 9990 §3.5: a report goes to every rua= URI. They are queued
	// together, with the rows marked, so a failure sends none and the next
	// interval tries again.
	return r.db.Transaction(func(tx *gorm.DB) error {
		for _, rua := range recipients {
			q := models.OutboundQueue{
				Sender:     meta.Email,
				Recipient:  rua,
				Domain:     domainOf(rua),
				RawMessage: buildReportEmail(meta.Email, rua, subject, filename, reportID, published.Domain, r.orgDomain, now, gz),
				Status:     "pending",
			}
			if err := tx.Create(&q).Error; err != nil {
				return err
			}
			slog.Info("dmarc reporter: queued aggregate report", "policy_domain", published.Domain, "rua", rua, "records", len(records))
		}
		return markReported(tx)
	})
}

// aggregateRecords converts stored evaluations into the library's neutral
// input: each names the signature and the SPF-checked domain dmarc_check
// recorded. A row recorded before those were kept names the reported-on
// domain for both, as the reports did then.
func aggregateRecords(records []models.DMARCAggregateRecord) []dmarc.AggregateRecord {
	out := make([]dmarc.AggregateRecord, len(records))
	for i, r := range records {
		out[i] = dmarc.AggregateRecord{
			Domain:      r.Domain,
			SourceIP:    r.SourceIP,
			HeaderFrom:  r.HeaderFrom,
			Disposition: r.Disposition,
			EnvelopeTo:  r.EnvelopeTo,
		}
		if dkimDomain := orDomain(r.DKIMDomain, r.DKIMResult, r.Domain); dkimDomain != "" {
			out[i].DKIM = []dmarc.DKIMAuth{{Domain: dkimDomain, Selector: r.DKIMSelector, Result: r.DKIMResult, Aligned: r.DKIMAligned}}
		}
		if spfDomain := orDomain(r.SPFDomain, r.SPFResult, r.Domain); spfDomain != "" {
			out[i].SPF = &dmarc.SPFAuth{Domain: spfDomain, Result: r.SPFResult, Aligned: r.SPFAligned}
		}
		if r.OverrideReason != "" {
			out[i].Reasons = []dmarc.PolicyOverrideReason{{Type: r.OverrideReason, Comment: r.OverrideComment}}
		}
	}
	return out
}

// orDomain is the domain a mechanism checked: the one recorded, or, for a row
// recorded before it was kept, the reported-on domain when the mechanism
// reached a result. A mechanism with no domain and no result did not run.
func orDomain(recorded, result, reported string) string {
	switch {
	case recorded != "":
		return recorded
	case result != "" && result != "none":
		return reported
	default:
		return ""
	}
}

// mailtoRUAs returns the addresses of the mailto: URIs in ruas, in order.
// Other schemes are left out: REST Mail sends reports by mail only, and RFC
// 9989 §4.7 has a receiver ignore a scheme it does not support.
func mailtoRUAs(ruas []string) []string {
	var out []string
	for _, u := range ruas {
		if len(u) < len("mailto:") || !strings.EqualFold(u[:len("mailto:")], "mailto:") {
			continue
		}
		addr, _, _ := strings.Cut(u[len("mailto:"):], "?")
		if addr = strings.TrimSpace(addr); addr != "" {
			out = append(out, addr)
		}
	}
	return out
}

func domainOf(addr string) string {
	if i := strings.LastIndex(addr, "@"); i >= 0 {
		return addr[i+1:]
	}
	return addr
}

// buildReportEmail builds an RFC 9990 §3.5.2 aggregate report email: a
// multipart/mixed message with a short text part and the gzipped report as an
// application/gzip attachment, under the name and the Subject §3.5.2 gives.
func buildReportEmail(from, to, subject, filename, reportID, policyDomain, orgDomain string, now time.Time, gz []byte) string {
	boundary := "=_dmarc_" + reportID
	b64 := base64.StdEncoding.EncodeToString(gz)

	var sb strings.Builder
	sb.WriteString("From: " + from + "\r\n")
	sb.WriteString("To: " + to + "\r\n")
	sb.WriteString("Subject: " + subject + "\r\n")
	sb.WriteString("Message-ID: <" + reportID + ">\r\n")
	sb.WriteString("Date: " + now.UTC().Format(time.RFC1123Z) + "\r\n")
	sb.WriteString("MIME-Version: 1.0\r\n")
	sb.WriteString("Content-Type: multipart/mixed; boundary=\"" + boundary + "\"\r\n")
	sb.WriteString("\r\n")

	sb.WriteString("--" + boundary + "\r\n")
	sb.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	sb.WriteString("\r\n")
	sb.WriteString(fmt.Sprintf("DMARC aggregate report for %s, submitted by %s.\r\n", policyDomain, orgDomain))

	sb.WriteString("--" + boundary + "\r\n")
	sb.WriteString("Content-Type: application/gzip\r\n")
	sb.WriteString("Content-Transfer-Encoding: base64\r\n")
	sb.WriteString("Content-Disposition: attachment; filename=\"" + filename + "\"\r\n")
	sb.WriteString("\r\n")
	for i := 0; i < len(b64); i += 76 {
		j := i + 76
		if j > len(b64) {
			j = len(b64)
		}
		sb.WriteString(b64[i:j] + "\r\n")
	}
	sb.WriteString("--" + boundary + "--\r\n")
	return sb.String()
}
