package dmarcreport

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/config"
	rmdb "github.com/restmail/restmail/internal/db"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// openReporterDB connects to the unit-test Postgres, skipping (never failing)
// when none is reachable, per the repo's DB-test convention.
func openReporterDB(t *testing.T) *gorm.DB {
	t.Helper()
	port, _ := strconv.Atoi(envOr("DB_PORT", "5432"))
	db, err := rmdb.Connect(&config.Config{
		DBHost: envOr("DB_HOST", "localhost"),
		DBPort: port,
		DBName: envOr("DB_NAME", "restmail"),
		DBUser: envOr("DB_USER", "restmail"),
		DBPass: envOr("DB_PASS", "restmail"),
	})
	if err != nil {
		t.Skipf("DMARC reporter DB test skipped: no database reachable (%v)", err)
	}
	if err := db.AutoMigrate(&models.DMARCAggregateRecord{}, &models.OutboundQueue{}); err != nil {
		t.Skipf("DMARC reporter DB test skipped: migrate failed (%v)", err)
	}
	return db
}

func TestReporter_ReportDomain(t *testing.T) {
	db := openReporterDB(t)
	run := time.Now().UnixNano()

	cases := []struct {
		name string
		// from is the reported-on (header From) domain; dns maps a TXT name to
		// its record.
		from     string
		dns      map[string]string
		sourceIP []string
		// override and overrideComment are the rows' policy-override reason.
		override, overrideComment string
		// wantTo is the rua address a report is queued for, "" for none.
		wantTo       string
		wantInReport []string
	}{
		{
			name:         "mail without a client IP is left out",
			from:         "local.test",
			dns:          map[string]string{"_dmarc.local.test": "v=DMARC1; p=reject; rua=mailto:agg@local.test"},
			sourceIP:     []string{"192.0.2.1", ""},
			wantTo:       "agg@local.test",
			wantInReport: []string{"<source_ip>192.0.2.1</source_ip>", "<count>1</count>"},
		},
		{
			name:     "only mail without a client IP",
			from:     "onlylocal.test",
			dns:      map[string]string{"_dmarc.onlylocal.test": "v=DMARC1; p=reject; rua=mailto:agg@onlylocal.test"},
			sourceIP: []string{""},
		},
		{
			name:     "an external rua= that did not authorize is not sent to",
			from:     "unauth.test",
			dns:      map[string]string{"_dmarc.unauth.test": "v=DMARC1; p=none; rua=mailto:agg@thirdparty.test"},
			sourceIP: []string{"192.0.2.2"},
		},
		{
			name: "an external rua= that authorized is sent to",
			from: "auth.test",
			dns: map[string]string{
				"_dmarc.auth.test":                         "v=DMARC1; p=none; rua=mailto:agg@thirdparty.test",
				"auth.test._report._dmarc.thirdparty.test": "v=DMARC1",
			},
			sourceIP: []string{"192.0.2.3"},
			wantTo:   "agg@thirdparty.test",
		},
		{
			name:         "a subdomain without a record reports to its organization's rua=",
			from:         "mail.org.test",
			dns:          map[string]string{"_dmarc.org.test": "v=DMARC1; p=reject; sp=quarantine; rua=mailto:agg@org.test"},
			sourceIP:     []string{"192.0.2.4"},
			wantTo:       "agg@org.test",
			wantInReport: []string{"<domain>org.test</domain>", "<sp>quarantine</sp>"},
		},
		{
			name:            "a policy override's reason is reported",
			from:            "fwd.test",
			dns:             map[string]string{"_dmarc.fwd.test": "v=DMARC1; p=reject; rua=mailto:agg@fwd.test"},
			sourceIP:        []string{"192.0.2.5"},
			override:        "trusted_forwarder",
			overrideComment: "arc=pass",
			wantTo:          "agg@fwd.test",
			wantInReport:    []string{"<type>trusted_forwarder</type>", "<comment>arc=pass</comment>"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A per-run label keeps rows apart from other runs on a shared DB.
			label := fmt.Sprintf("r%d", run)
			rename := func(s string) string { return strings.ReplaceAll(s, ".test", "-"+label+".test") }
			from := rename(tc.from)
			dns := map[string]string{}
			for k, v := range tc.dns {
				dns[rename(k)] = rename(v)
			}
			wantTo := rename(tc.wantTo)
			t.Cleanup(func() {
				db.Where("domain = ?", from).Delete(&models.DMARCAggregateRecord{})
				db.Where("sender = ? AND recipient LIKE ?", "dmarc-reports@reporter.test", "%"+label+"%").Delete(&models.OutboundQueue{})
			})

			for _, ip := range tc.sourceIP {
				if err := db.Create(&models.DMARCAggregateRecord{
					Domain: from, SourceIP: ip, HeaderFrom: "user@" + from, Disposition: "none", Policy: "reject",
					DKIMResult: "pass", DKIMAligned: true, SPFResult: "pass", SPFAligned: true,
					OverrideReason: tc.override, OverrideComment: tc.overrideComment,
				}).Error; err != nil {
					t.Fatal(err)
				}
			}

			r := NewReporter(db, time.Hour, "reporter.test")
			r.lookupTXT = func(name string) ([]string, error) {
				if rec, ok := dns[name]; ok {
					return []string{rec}, nil
				}
				return nil, nil
			}
			if err := r.reportDomain(from, time.Now()); err != nil {
				t.Fatalf("reportDomain: %v", err)
			}

			var queued []models.OutboundQueue
			db.Where("sender = ? AND raw_message LIKE ?", "dmarc-reports@reporter.test", "%Report Domain: "+from+" %").Find(&queued)
			switch {
			case wantTo == "" && len(queued) != 0:
				t.Errorf("queued a report to %s, want none", queued[0].Recipient)
			case wantTo != "" && (len(queued) != 1 || queued[0].Recipient != wantTo):
				t.Errorf("queued %d reports (%+v), want one to %s", len(queued), queued, wantTo)
			}
			if len(queued) == 1 && len(tc.wantInReport) > 0 {
				report := reportXML(t, queued[0].RawMessage)
				for _, want := range tc.wantInReport {
					if !strings.Contains(report, rename(want)) {
						t.Errorf("report lacks %s:\n%s", rename(want), report)
					}
				}
			}

			var pending int64
			db.Model(&models.DMARCAggregateRecord{}).Where("domain = ? AND reported = ?", from, false).Count(&pending)
			if pending != 0 {
				t.Errorf("%d rows left unreported", pending)
			}
		})
	}
}
