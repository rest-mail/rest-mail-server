package main

import (
	"errors"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func prepareDMARCReport(database *gorm.DB, domain string) error {
	if domain != "restmail.test" {
		return errors.New("DMARC fixtures require restmail.test")
	}
	var hosted models.Domain
	if err := database.Where("name = ?", domain).First(&hosted).Error; err != nil {
		return err
	}
	report := models.DMARCReportReceived{DomainID: hosted.ID, Reporter: "reporter.test", ReportingOrg: "Reporter", ReportID: "daily-1", StartDate: time.Unix(1793577600, 0).UTC(), EndDate: time.Unix(1793663999, 0).UTC(), ReceivedAt: time.Unix(1793664000, 0).UTC(), PolicyPublished: models.DMARCReceivedPolicy{Domain: domain, P: "none"}, Records: []models.DMARCReceivedRecord{{Row: models.DMARCReceivedRow{SourceIP: "192.0.2.1", Count: 12, PolicyEvaluated: models.DMARCReceivedEvaluation{Disposition: "none", DKIM: "pass", SPF: "fail"}}, Identifiers: models.DMARCReceivedIdentifiers{HeaderFrom: domain}, AuthResults: models.DMARCReceivedAuthResults{DKIM: []models.DMARCReceivedDKIM{{Domain: domain, Selector: "mail", Result: "pass"}}}}}}
	return database.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "reporter"}, {Name: "report_id"}}, DoNothing: true}).Create(&report).Error
}
