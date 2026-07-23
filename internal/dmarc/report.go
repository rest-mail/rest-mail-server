// Package dmarc builds and delivers RFC 7489 DMARC aggregate (rua) reports from
// the per-message evaluations captured by the dmarc_check filter.
package dmarc

import (
	"bytes"
	"compress/gzip"
	"encoding/xml"
	"fmt"

	"github.com/restmail/restmail/internal/db/models"
)

// Feedback is the root element of an RFC 7489 aggregate report.
type Feedback struct {
	XMLName         xml.Name        `xml:"feedback"`
	Version         string          `xml:"version,omitempty"`
	ReportMetadata  ReportMetadata  `xml:"report_metadata"`
	PolicyPublished PolicyPublished `xml:"policy_published"`
	Records         []ReportRecord  `xml:"record"`
}

// ReportMetadata identifies the reporting organization and period.
type ReportMetadata struct {
	OrgName   string    `xml:"org_name"`
	Email     string    `xml:"email"`
	ReportID  string    `xml:"report_id"`
	DateRange DateRange `xml:"date_range"`
}

// DateRange is the reporting period as UNIX epoch seconds.
type DateRange struct {
	Begin int64 `xml:"begin"`
	End   int64 `xml:"end"`
}

// PolicyPublished is the DMARC record the reported-on domain published.
type PolicyPublished struct {
	Domain string `xml:"domain"`
	ADKIM  string `xml:"adkim,omitempty"`
	ASPF   string `xml:"aspf,omitempty"`
	P      string `xml:"p"`
	SP     string `xml:"sp,omitempty"`
	PCT    int    `xml:"pct,omitempty"`
}

// ReportRecord is one aggregated row: a source IP + evaluation + counts.
type ReportRecord struct {
	Row         Row         `xml:"row"`
	Identifiers Identifiers `xml:"identifiers"`
	AuthResults AuthResults `xml:"auth_results"`
}

type Row struct {
	SourceIP        string          `xml:"source_ip"`
	Count           int             `xml:"count"`
	PolicyEvaluated PolicyEvaluated `xml:"policy_evaluated"`
}

type PolicyEvaluated struct {
	Disposition string `xml:"disposition"`
	DKIM        string `xml:"dkim"`
	SPF         string `xml:"spf"`
}

type Identifiers struct {
	HeaderFrom string `xml:"header_from"`
}

type AuthResults struct {
	DKIM []DKIMResult `xml:"dkim,omitempty"`
	SPF  []SPFResult  `xml:"spf,omitempty"`
}

type DKIMResult struct {
	Domain string `xml:"domain"`
	Result string `xml:"result"`
}

type SPFResult struct {
	Domain string `xml:"domain"`
	Result string `xml:"result"`
}

// evaluated returns the DMARC-aligned pass/fail: a mechanism only counts as
// passing DMARC when it both passed AND aligned with the From domain.
func evaluated(result string, aligned bool) string {
	if result == "pass" && aligned {
		return "pass"
	}
	return "fail"
}

// AggregateRecords groups raw per-message evaluations into report rows by
// (source IP, disposition, evaluated dkim/spf), summing counts.
func AggregateRecords(records []models.DMARCAggregateRecord) []ReportRecord {
	type key struct {
		sourceIP, headerFrom, disposition, dkimEval, spfEval, dkimAuth, spfAuth, authDomain string
	}
	order := []key{}
	counts := map[key]int{}
	for _, r := range records {
		k := key{
			sourceIP:    r.SourceIP,
			headerFrom:  r.HeaderFrom,
			disposition: r.Disposition,
			dkimEval:    evaluated(r.DKIMResult, r.DKIMAligned),
			spfEval:     evaluated(r.SPFResult, r.SPFAligned),
			dkimAuth:    r.DKIMResult,
			spfAuth:     r.SPFResult,
			authDomain:  r.Domain,
		}
		if _, seen := counts[k]; !seen {
			order = append(order, k)
		}
		counts[k]++
	}

	out := make([]ReportRecord, 0, len(order))
	for _, k := range order {
		hf := k.headerFrom
		if hf == "" {
			hf = k.authDomain
		}
		out = append(out, ReportRecord{
			Row: Row{
				SourceIP: k.sourceIP,
				Count:    counts[k],
				PolicyEvaluated: PolicyEvaluated{
					Disposition: k.disposition,
					DKIM:        k.dkimEval,
					SPF:         k.spfEval,
				},
			},
			Identifiers: Identifiers{HeaderFrom: hf},
			AuthResults: AuthResults{
				DKIM: []DKIMResult{{Domain: k.authDomain, Result: k.dkimAuth}},
				SPF:  []SPFResult{{Domain: k.authDomain, Result: k.spfAuth}},
			},
		})
	}
	return out
}

// BuildReport assembles an RFC 7489 aggregate report XML document (with the XML
// declaration prepended).
func BuildReport(meta ReportMetadata, policy PolicyPublished, records []models.DMARCAggregateRecord) ([]byte, error) {
	fb := Feedback{
		Version:         "1.0",
		ReportMetadata:  meta,
		PolicyPublished: policy,
		Records:         AggregateRecords(records),
	}
	body, err := xml.MarshalIndent(fb, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal dmarc report: %w", err)
	}
	return append([]byte(xml.Header), body...), nil
}

// Gzip compresses report bytes for the report attachment (reports are delivered
// as application/gzip per RFC 7489 §7.2.1).
func Gzip(data []byte) ([]byte, error) {
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
