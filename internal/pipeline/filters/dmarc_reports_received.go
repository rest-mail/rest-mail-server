package filters

import (
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"mime"
	"net/mail"
	"strings"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/dmarcreceive"
	rmail "github.com/rest-mail/rest-mail-server/internal/mail"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
	"gorm.io/gorm/clause"
)

type dmarcReportsReceivedFilter struct{}

func init() {
	pipeline.DefaultRegistry.Register("dmarc_reports_received", NewDMARCReportsReceived)
}

func NewDMARCReportsReceived(_ []byte) (pipeline.Filter, error) {
	return &dmarcReportsReceivedFilter{}, nil
}

func (f *dmarcReportsReceivedFilter) Name() string              { return "dmarc_reports_received" }
func (f *dmarcReportsReceivedFilter) Type() pipeline.FilterType { return pipeline.FilterTypeAction }

func (f *dmarcReportsReceivedFilter) Execute(ctx context.Context, email *pipeline.EmailJSON) (*pipeline.FilterResult, error) {
	result := &pipeline.FilterResult{Type: f.Type(), Action: pipeline.ActionContinue, Log: pipeline.FilterLog{Filter: f.Name(), Result: "skipped"}}
	if email.Envelope.Direction != "inbound" || len(email.Headers.From) != 1 {
		return result, nil
	}
	from, err := mail.ParseAddress(email.Headers.From[0].Address)
	if err != nil {
		return result, nil
	}
	at := strings.LastIndexByte(from.Address, '@')
	if at < 0 {
		return result, nil
	}
	reporter, err := rmail.DomainASCII(strings.TrimSuffix(from.Address[at+1:], "."))
	if err != nil || !pipeline.IsHostName(reporter) {
		return result, nil
	}
	reporter = strings.ToLower(reporter)
	aligned := false
	for _, verdict := range pipeline.AuthResultsFromContext(ctx).Find(pipeline.AuthDMARC, pipeline.PropHeaderFrom) {
		if verdict.Result == "pass" && strings.EqualFold(strings.TrimSuffix(verdict.Domain, "."), reporter) {
			aligned = true
		}
	}
	if !aligned {
		for _, recipient := range email.Envelope.RcptTo {
			if !strings.HasPrefix(strings.ToLower(recipient), "postmaster@") {
				continue
			}
			for _, attachment := range email.Attachments {
				media, _, err := mime.ParseMediaType(attachment.ContentType)
				if err == nil && (media == "application/gzip" || media == "application/zip" || media == "text/xml") {
					f.dropped(result, errors.New("report mail lacks aligned DMARC pass"))
					return result, nil
				}
			}
		}
		return result, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	db := pipeline.DBFromContext(ctx)
	if db == nil {
		return result, nil
	}
	db = db.WithContext(ctx)
	remaining, attachments := dmarcreceive.MaxBytes, 0
	for _, attachment := range email.Attachments {
		media, _, err := mime.ParseMediaType(attachment.ContentType)
		if err != nil || media != "application/gzip" && media != "application/zip" && media != "text/xml" {
			continue
		}
		attachments++
		if attachments > dmarcreceive.MaxZipEntries || base64.StdEncoding.DecodedLen(len(attachment.Content)) > remaining+2 {
			f.dropped(result, errors.New("message report attachment limit"))
			break
		}
		data, err := base64.StdEncoding.DecodeString(attachment.Content)
		if err != nil {
			f.dropped(result, errors.New("invalid report attachment encoding"))
			continue
		}
		remaining -= len(data)
		if remaining < 0 {
			f.dropped(result, errors.New("message report size limit"))
			break
		}
		reports, err := dmarcreceive.Parse(media, data)
		if err != nil {
			f.dropped(result, err)
			continue
		}
		for _, report := range reports {
			addressed := false
			for _, recipient := range email.Envelope.RcptTo {
				if strings.EqualFold(recipient, "postmaster@"+report.PolicyPublished.Domain) {
					addressed = true
				}
			}
			if !addressed {
				continue
			}
			var domain models.Domain
			if err := db.Where("name = ?", report.PolicyPublished.Domain).First(&domain).Error; err != nil {
				f.dropped(result, errors.New("report domain lookup failed"))
				continue
			}
			report.DomainID, report.Reporter, report.ReceivedAt = domain.ID, reporter, time.Now().UTC()
			if err := db.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "reporter"}, {Name: "report_id"}}, DoNothing: true}).Create(&report).Error; err != nil {
				f.dropped(result, errors.New("report storage failed"))
				continue
			}
			result.Log.Result = "processed"
		}
	}
	return result, nil
}

func (f *dmarcReportsReceivedFilter) dropped(result *pipeline.FilterResult, err error) {
	result.Log.Result = "dropped"
	result.Log.Detail = err.Error()
	slog.Warn("DMARC aggregate report dropped; mail delivery continues", "reason", err.Error())
}
