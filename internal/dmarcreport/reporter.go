// Package dmarcreport runs the periodic worker that emits RFC 7489 DMARC
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

	"github.com/rest-mail/go-dmarc"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

// Reporter periodically emits RFC 7489 DMARC aggregate (rua) reports for the
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
	for _, d := range domains {
		if err := r.reportDomain(d, now); err != nil {
			slog.Warn("dmarc reporter: domain failed", "domain", d, "error", err)
		}
	}
}

func (r *Reporter) reportDomain(domain string, now time.Time) error {
	var records []models.DMARCAggregateRecord
	if err := r.db.Where("domain = ? AND reported = ?", domain, false).
		Order("created_at ASC").Find(&records).Error; err != nil {
		return err
	}
	if len(records) == 0 {
		return nil
	}

	markReported := func() error {
		ids := make([]uint, len(records))
		for i, rec := range records {
			ids[i] = rec.ID
		}
		return r.db.Model(&models.DMARCAggregateRecord{}).Where("id IN ?", ids).Update("reported", true).Error
	}

	rua := r.lookupRUA(domain)
	if rua == "" {
		// Domain publishes no rua address — nothing to send; mark reported so the
		// records don't accumulate forever.
		return markReported()
	}

	begin := records[0].CreatedAt.Unix()
	end := now.Unix()
	reportID := fmt.Sprintf("%d.%s@%s", begin, domain, r.orgDomain)
	meta := dmarc.ReportMetadata{
		OrgName:   r.orgDomain,
		Email:     "dmarc-reports@" + r.orgDomain,
		ReportID:  reportID,
		DateRange: dmarc.DateRange{Begin: begin, End: end},
	}
	policy := dmarc.PolicyPublished{Domain: domain, ADKIM: "r", ASPF: "r", P: records[0].Policy, PCT: 100}

	xmlBytes, err := dmarc.BuildReport(meta, policy, aggregateRecords(records))
	if err != nil {
		return err
	}
	gz, err := dmarc.Gzip(xmlBytes)
	if err != nil {
		return err
	}

	raw := buildReportEmail(meta.Email, rua, domain, r.orgDomain, reportID, begin, end, now, gz)
	q := models.OutboundQueue{
		Sender:     meta.Email,
		Recipient:  rua,
		Domain:     domainOf(rua),
		RawMessage: raw,
		Status:     "pending",
	}
	if err := r.db.Create(&q).Error; err != nil {
		return err
	}
	slog.Info("dmarc reporter: queued aggregate report", "domain", domain, "rua", rua, "records", len(records))
	return markReported()
}

// aggregateRecords converts stored evaluations into the library's neutral input.
func aggregateRecords(records []models.DMARCAggregateRecord) []dmarc.AggregateRecord {
	out := make([]dmarc.AggregateRecord, len(records))
	for i, r := range records {
		out[i] = dmarc.AggregateRecord{
			Domain:      r.Domain,
			SourceIP:    r.SourceIP,
			HeaderFrom:  r.HeaderFrom,
			Disposition: r.Disposition,
			// go-dmarc v0.2.0 reshaped the scalar DKIM/SPF result+alignment into
			// per-mechanism slices (DKIMAuth/SPFAuth). This store keeps only one
			// DKIM and one SPF outcome per message, so each maps to a single-element
			// slice. The result and alignment values — which drive policy_evaluated —
			// are preserved exactly. The store never recorded the signature's d=
			// selector nor the SPF-checked (mfrom/HELO) domain, so those best-effort
			// to the reported-on domain; SPF scope is the envelope MAIL FROM ("mfrom").
			DKIM: []dmarc.DKIMAuth{{
				Domain:  r.Domain,
				Result:  r.DKIMResult,
				Aligned: r.DKIMAligned,
			}},
			SPF: []dmarc.SPFAuth{{
				Domain:  r.Domain,
				Scope:   "mfrom",
				Result:  r.SPFResult,
				Aligned: r.SPFAligned,
			}},
		}
	}
	return out
}

func (r *Reporter) lookupRUA(domain string) string {
	txts, err := r.lookupTXT("_dmarc." + domain)
	if err != nil {
		return ""
	}
	for _, t := range txts {
		if strings.HasPrefix(t, "v=DMARC1") {
			return parseRUA(t)
		}
	}
	return ""
}

// parseRUA extracts the first mailto: address from a DMARC record's rua= tag.
func parseRUA(record string) string {
	for _, part := range strings.Split(record, ";") {
		part = strings.TrimSpace(part)
		if !strings.HasPrefix(part, "rua=") {
			continue
		}
		for _, u := range strings.Split(strings.TrimPrefix(part, "rua="), ",") {
			u = strings.TrimSpace(u)
			if strings.HasPrefix(u, "mailto:") {
				addr := strings.TrimPrefix(u, "mailto:")
				if i := strings.IndexByte(addr, '!'); i >= 0 { // strip !size limit
					addr = addr[:i]
				}
				return strings.TrimSpace(addr)
			}
		}
	}
	return ""
}

func domainOf(addr string) string {
	if i := strings.LastIndex(addr, "@"); i >= 0 {
		return addr[i+1:]
	}
	return addr
}

// buildReportEmail builds an RFC 7489 §7.2.1 aggregate report email: a
// multipart/mixed message with a short text part and the gzipped report as an
// application/gzip attachment named per the report-file convention.
func buildReportEmail(from, to, reportedDomain, orgDomain, reportID string, begin, end int64, now time.Time, gz []byte) string {
	boundary := "=_dmarc_" + reportID
	filename := fmt.Sprintf("%s!%s!%d!%d.xml.gz", orgDomain, reportedDomain, begin, end)
	b64 := base64.StdEncoding.EncodeToString(gz)

	var sb strings.Builder
	sb.WriteString("From: " + from + "\r\n")
	sb.WriteString("To: " + to + "\r\n")
	sb.WriteString(fmt.Sprintf("Subject: Report Domain: %s Submitter: %s Report-ID: %s\r\n", reportedDomain, orgDomain, reportID))
	sb.WriteString("Message-ID: <" + reportID + ">\r\n")
	sb.WriteString("Date: " + now.UTC().Format(time.RFC1123Z) + "\r\n")
	sb.WriteString("MIME-Version: 1.0\r\n")
	sb.WriteString("Content-Type: multipart/mixed; boundary=\"" + boundary + "\"\r\n")
	sb.WriteString("\r\n")

	sb.WriteString("--" + boundary + "\r\n")
	sb.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	sb.WriteString("\r\n")
	sb.WriteString(fmt.Sprintf("DMARC aggregate report for %s, submitted by %s.\r\n", reportedDomain, orgDomain))

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
