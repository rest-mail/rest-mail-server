package queue

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/outbound"
	"gorm.io/gorm"
)

// tlsReportInterval is how often the worker looks for days to report on. A
// day is reported in the first pass after it ends, and a report that could
// not be delivered is tried again each pass for a day after (RFC 8460 §4.1,
// §5.5).
const tlsReportInterval = time.Hour

// tlsReportGiveUp is how long after its day ends a report that could not be
// delivered is still tried (§5.5: "for up to 24 hours after the initial
// attempt").
const tlsReportGiveUp = 48 * time.Hour

// tlsReportLock is the Postgres advisory lock one worker takes for a pass, so
// that several gateways do not each send the same report.
const tlsReportLock = 8460

// tlsReportLoop sends the TLS-RPT reports of each finished day until the
// worker shuts down.
func (w *Worker) tlsReportLoop() {
	ticker := time.NewTicker(tlsReportInterval)
	defer ticker.Stop()
	for {
		select {
		case <-w.shutdown:
			return
		case <-ticker.C:
			w.sendTLSReports(time.Now())
		}
	}
}

// sendTLSReports reports, for each finished UTC day, the sessions to each
// Policy Domain, and deletes them once a report was accepted, the domain asks
// for none, or the report has been tried for long enough.
func (w *Worker) sendTLSReports(now time.Time) {
	err := w.db.Transaction(func(tx *gorm.DB) error {
		var locked bool
		if err := tx.Raw("SELECT pg_try_advisory_xact_lock(?)", tlsReportLock).Scan(&locked).Error; err != nil || !locked {
			return err
		}
		type pending struct{ Day, PolicyDomain string }
		var days []pending
		if err := tx.Model(&models.TLSRPTSession{}).Where("day < ?", now.UTC().Format(tlsrptDay)).
			Distinct("day", "policy_domain").Order("day, policy_domain").Find(&days).Error; err != nil {
			return err
		}
		for _, p := range days {
			done, err := w.reportTLSDay(tx, p.Day, p.PolicyDomain, now)
			if err != nil {
				slog.Warn("queue: TLS-RPT report not sent", "domain", p.PolicyDomain, "day", p.Day, "error", err)
			}
			if done || tlsReportExpired(p.Day, now) {
				if err := tx.Where("day = ? AND policy_domain = ?", p.Day, p.PolicyDomain).Delete(&models.TLSRPTSession{}).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		slog.Error("queue: TLS-RPT reporting failed", "error", err)
	}
}

// tlsReportExpired reports whether day ended long enough ago to stop trying.
func tlsReportExpired(day string, now time.Time) bool {
	start, err := time.Parse(tlsrptDay, day)
	return err != nil || now.Sub(start.Add(24*time.Hour)) > tlsReportGiveUp
}

// reportTLSDay sends one day's report for domain to each address its
// _smtp._tls record lists. done is true when no more is to be done with the
// sessions: an address accepted the report, or the domain asks for none.
func (w *Worker) reportTLSDay(tx *gorm.DB, day, domain string, now time.Time) (done bool, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ruas, err := w.tlsrptRecord(ctx, domain)
	if err != nil {
		return false, err
	}
	if len(ruas) == 0 {
		return true, nil
	}
	var sessions []models.TLSRPTSession
	if err := tx.Where("day = ? AND policy_domain = ?", day, domain).Order("id").Find(&sessions).Error; err != nil {
		return false, err
	}
	submitter, err := w.tlsReportDomain(tx)
	if err != nil {
		return false, err
	}
	report, err := buildTLSReport(day, domain, submitter, sessions)
	if err != nil {
		return false, err
	}
	body, err := json.Marshal(report)
	if err != nil {
		return false, err
	}
	gz, err := gzipBytes(body)
	if err != nil {
		return false, err
	}
	start, _ := time.Parse(tlsrptDay, day)
	filename := fmt.Sprintf("%s!%s!%d!%d.json.gz", submitter, domain, start.Unix(), start.Add(24*time.Hour-time.Second).Unix())

	var errs []error
	for _, rua := range ruas {
		var err error
		switch {
		case strings.HasPrefix(strings.ToLower(rua), "https:"):
			err = w.postTLSReport(ctx, rua, gz)
		default:
			err = w.queueTLSReport(tx, rua, submitter, domain, report.ReportID, filename, gz, now)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", rua, err))
			continue
		}
		done = true
		slog.Info("queue: TLS-RPT report sent", "domain", domain, "day", day, "rua", rua, "sessions", len(sessions))
	}
	return done, errors.Join(errs...)
}

// tlsrptRecord returns the report addresses domain's _smtp._tls record lists
// (RFC 8460 §3): its mailto: and https: rua URIs, in order. A domain without
// exactly one record starting v=TLSRPTv1 asks for no reports. A DNS failure
// other than no such name is an error, to try again later.
func (w *Worker) tlsrptRecord(ctx context.Context, domain string) ([]string, error) {
	lookup := net.DefaultResolver.LookupTXT
	if w.txtResolver != nil {
		lookup = w.txtResolver
	}
	txts, err := lookup(ctx, "_smtp._tls."+domain)
	if err != nil {
		var dnsErr *net.DNSError
		if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
			return nil, nil
		}
		return nil, err
	}
	return parseTLSRPTRecord(txts), nil
}

// parseTLSRPTRecord reads the rua= URIs of the one TXT record starting
// v=TLSRPTv1 (RFC 8460 §3): records that do not start so are discarded, and
// with not exactly one left the domain asks for no reports. Unknown fields are
// ignored, and so are URIs that are neither mailto: nor https:.
func parseTLSRPTRecord(txts []string) []string {
	var records []string
	for _, t := range txts {
		if first, _, _ := strings.Cut(t, ";"); strings.TrimSpace(first) == "v=TLSRPTv1" {
			records = append(records, t)
		}
	}
	if len(records) != 1 {
		return nil
	}
	var ruas []string
	for _, field := range strings.Split(records[0], ";")[1:] {
		name, value, ok := strings.Cut(strings.TrimSpace(field), "=")
		if !ok || name != "rua" {
			continue
		}
		for _, u := range strings.Split(value, ",") {
			u = strings.TrimSpace(u)
			parsed, err := url.Parse(u)
			if err != nil {
				continue
			}
			switch strings.ToLower(parsed.Scheme) {
			case "mailto":
				if strings.Contains(parsed.Opaque, "@") {
					ruas = append(ruas, u)
				}
			case "https":
				if parsed.Host != "" {
					ruas = append(ruas, u)
				}
			}
		}
	}
	return ruas
}

// tlsReport is the JSON report of RFC 8460 §4.4.
type tlsReport struct {
	OrganizationName string            `json:"organization-name"`
	DateRange        tlsReportRange    `json:"date-range"`
	ContactInfo      string            `json:"contact-info"`
	ReportID         string            `json:"report-id"`
	Policies         []tlsReportPolicy `json:"policies"`
}

type tlsReportRange struct {
	Start string `json:"start-datetime"`
	End   string `json:"end-datetime"`
}

type tlsReportPolicy struct {
	Policy         tlsAppliedPolicy   `json:"policy"`
	Summary        tlsReportSummary   `json:"summary"`
	FailureDetails []tlsFailureDetail `json:"failure-details,omitempty"`
}

type tlsAppliedPolicy struct {
	PolicyType   string   `json:"policy-type"`
	PolicyString []string `json:"policy-string,omitempty"`
	PolicyDomain string   `json:"policy-domain"`
	// MXHost is the policy's MX patterns, a JSON array of strings (§4.4).
	MXHost []string `json:"mx-host,omitempty"`
}

type tlsReportSummary struct {
	Successful int `json:"total-successful-session-count"`
	Failed     int `json:"total-failure-session-count"`
}

type tlsFailureDetail struct {
	ResultType          string `json:"result-type"`
	SendingMTAIP        string `json:"sending-mta-ip,omitempty"`
	ReceivingMXHostname string `json:"receiving-mx-hostname,omitempty"`
	ReceivingIP         string `json:"receiving-ip,omitempty"`
	FailedSessionCount  int    `json:"failed-session-count"`
	FailureReasonCode   string `json:"failure-reason-code,omitempty"`
}

// buildTLSReport aggregates a day's sessions to domain into its report: one
// entry for each policy applied, with its success and failure counts and a
// failure detail for each result type, address and reason seen (§4.4). The
// report covers the whole UTC day (§4.1) and comes from submitter.
func buildTLSReport(day, domain, submitter string, sessions []models.TLSRPTSession) (tlsReport, error) {
	start, err := time.Parse(tlsrptDay, day)
	if err != nil {
		return tlsReport{}, err
	}
	report := tlsReport{
		OrganizationName: submitter,
		DateRange: tlsReportRange{
			Start: start.Format(time.RFC3339),
			End:   start.Add(24*time.Hour - time.Second).Format(time.RFC3339),
		},
		ContactInfo: "tlsrpt@" + submitter,
		ReportID:    fmt.Sprintf("%s.%s@%s", day, domain, submitter),
	}
	type policyKey struct{ policyType, policyString, mxHost string }
	type detailKey struct{ result, sendingIP, mx, receivingIP, reason string }
	index := map[policyKey]int{}
	details := map[policyKey]map[detailKey]int{}
	for _, s := range sessions {
		pk := policyKey{s.PolicyType, s.PolicyString, s.MXHost}
		i, seen := index[pk]
		if !seen {
			i = len(report.Policies)
			index[pk] = i
			applied := tlsAppliedPolicy{PolicyType: s.PolicyType, PolicyDomain: domain}
			_ = json.Unmarshal([]byte(s.PolicyString), &applied.PolicyString)
			_ = json.Unmarshal([]byte(s.MXHost), &applied.MXHost)
			report.Policies = append(report.Policies, tlsReportPolicy{Policy: applied})
			details[pk] = map[detailKey]int{}
		}
		if s.ResultType == "" {
			report.Policies[i].Summary.Successful++
			continue
		}
		report.Policies[i].Summary.Failed++
		details[pk][detailKey{s.ResultType, s.SendingIP, s.ReceivingMX, s.ReceivingIP, s.FailureReason}]++
	}
	for pk, i := range index {
		for dk, n := range details[pk] {
			report.Policies[i].FailureDetails = append(report.Policies[i].FailureDetails, tlsFailureDetail{
				ResultType: dk.result, SendingMTAIP: dk.sendingIP, ReceivingMXHostname: dk.mx,
				ReceivingIP: dk.receivingIP, FailedSessionCount: n, FailureReasonCode: dk.reason,
			})
		}
		sort.Slice(report.Policies[i].FailureDetails, func(a, b int) bool {
			x, y := report.Policies[i].FailureDetails[a], report.Policies[i].FailureDetails[b]
			if x.ResultType != y.ResultType {
				return x.ResultType < y.ResultType
			}
			return x.ReceivingMXHostname+x.ReceivingIP+x.SendingMTAIP+x.FailureReasonCode <
				y.ReceivingMXHostname+y.ReceivingIP+y.SendingMTAIP+y.FailureReasonCode
		})
	}
	return report, nil
}

// postTLSReport delivers a gzipped report by HTTPS POST (RFC 8460 §5.4),
// through the same outbound guard as mail delivery: a rua= URL is the
// domain's to choose, so it may not reach this network's own hosts.
func (w *Worker) postTLSReport(ctx context.Context, rua string, gz []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rua, bytes.NewReader(gz))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/tlsrpt+gzip")
	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			DialContext:         w.newGuardedDialer(10 * time.Second).DialContext,
			TLSHandshakeTimeout: 10 * time.Second,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	if w.tlsReportClient != nil {
		client = w.tlsReportClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

// queueTLSReport queues a report for a mailto: address (RFC 8460 §5.3): a
// multipart/report of report-type tlsrpt, with the TLS-Report-Domain and
// TLS-Report-Submitter fields, sent as submitter, whose DKIM key signs it, and
// marked to go despite any TLS failure (§3).
func (w *Worker) queueTLSReport(tx *gorm.DB, rua, submitter, domain, reportID, filename string, gz []byte, now time.Time) error {
	signed, err := outbound.HasDKIMKey(tx, submitter)
	if err != nil {
		return err
	}
	if !signed {
		// §3: a report by mail "MUST contain a valid DKIM signature by the
		// reporting domain", and one without is ignored.
		return fmt.Errorf("%s has no DKIM key to sign a report with", submitter)
	}
	u, err := url.Parse(rua)
	if err != nil {
		return err
	}
	to, _, _ := strings.Cut(u.Opaque, "?")
	if to, err = url.PathUnescape(to); err != nil {
		return err
	}
	from := "tlsrpt@" + submitter
	return tx.Create(&models.OutboundQueue{
		Sender:        from,
		SendingDomain: submitter,
		Recipient:     to,
		Domain:        domainOf(to),
		RawMessage:    tlsReportMessage(from, to, submitter, domain, reportID, filename, gz, now),
		Status:        "pending",
		TLSReport:     true,
	}).Error
}

// tlsReportMessage is the report mail of RFC 8460 §5.3.
func tlsReportMessage(from, to, submitter, domain, reportID, filename string, gz []byte, now time.Time) string {
	boundary := "=_tlsrpt_" + strings.NewReplacer("@", "_", ".", "_").Replace(reportID)
	var b strings.Builder
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + to + "\r\n")
	b.WriteString("Date: " + now.UTC().Format(time.RFC1123Z) + "\r\n")
	b.WriteString("Subject: Report Domain: " + domain + " Submitter: " + submitter + " Report-ID: <" + reportID + ">\r\n")
	b.WriteString("Message-ID: <" + reportID + ">\r\n")
	b.WriteString("TLS-Report-Domain: " + domain + "\r\n")
	b.WriteString("TLS-Report-Submitter: " + submitter + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: multipart/report; report-type=\"tlsrpt\"; boundary=\"" + boundary + "\"\r\n")
	b.WriteString("\r\n")
	b.WriteString("--" + boundary + "\r\n")
	b.WriteString("Content-Type: text/plain; charset=us-ascii\r\n\r\n")
	b.WriteString("This is an aggregate TLS report from " + submitter + " for " + domain + ".\r\n")
	b.WriteString("--" + boundary + "\r\n")
	b.WriteString("Content-Type: application/tlsrpt+gzip\r\n")
	b.WriteString("Content-Transfer-Encoding: base64\r\n")
	b.WriteString("Content-Disposition: attachment; filename=\"" + filename + "\"\r\n\r\n")
	b64 := base64.StdEncoding.EncodeToString(gz)
	for i := 0; i < len(b64); i += 76 {
		b.WriteString(b64[i:min(i+76, len(b64))] + "\r\n")
	}
	b.WriteString("--" + boundary + "--\r\n")
	return b.String()
}

// tlsReportDomain is the domain REST Mail reports as: the hosted domain the
// gateway's host name is, or the longest it is under, as the reports must be
// signed by the reporting domain (§3); the host name itself when it is under
// none.
func (w *Worker) tlsReportDomain(tx *gorm.DB) (string, error) {
	host := strings.ToLower(strings.TrimSuffix(w.hostname, "."))
	var names []string
	if err := tx.Model(&models.Domain{}).Where("active = ?", true).Pluck("name", &names).Error; err != nil {
		return "", err
	}
	best := ""
	for _, n := range names {
		n = strings.ToLower(n)
		if (host == n || strings.HasSuffix(host, "."+n)) && len(n) > len(best) {
			best = n
		}
	}
	if best == "" {
		return host, nil
	}
	return best, nil
}

// domainOf is the domain of an address, after its last @.
func domainOf(addr string) string {
	if i := strings.LastIndexByte(addr, '@'); i >= 0 {
		return addr[i+1:]
	}
	return addr
}

// gzipBytes compresses a report (RFC 8460 §5.2).
func gzipBytes(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(data); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
