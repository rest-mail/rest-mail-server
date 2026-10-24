package dmarcreport

import (
	"fmt"
	"os"
	"reflect"
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
		// policyDomain is the report's DMARC Policy Domain, from when empty.
		policyDomain string
		// wantTo are the rua addresses a report is queued for.
		wantTo       []string
		wantInReport []string
	}{
		{
			name:     "mail without a client IP is left out",
			from:     "local.test",
			dns:      map[string]string{"_dmarc.local.test": "v=DMARC1; p=reject; rua=mailto:agg@local.test"},
			sourceIP: []string{"192.0.2.1", ""},
			wantTo:   []string{"agg@local.test"},
			wantInReport: []string{"<source_ip>192.0.2.1</source_ip>", "<count>1</count>", `<feedback xmlns="urn:ietf:params:xml:ns:dmarc-2.0">`,
				"<discovery_method>treewalk</discovery_method>", "<generator>REST Mail "},
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
			wantTo:   []string{"agg@thirdparty.test"},
		},
		{
			name:         "a subdomain without a record reports to its organization's rua=",
			from:         "mail.org.test",
			dns:          map[string]string{"_dmarc.org.test": "v=DMARC1; p=reject; sp=quarantine; rua=mailto:agg@org.test"},
			sourceIP:     []string{"192.0.2.4"},
			policyDomain: "org.test",
			wantTo:       []string{"agg@org.test"},
			wantInReport: []string{"<domain>org.test</domain>", "<sp>quarantine</sp>", "<header_from>user@mail.org.test</header_from>"},
		},
		{
			name:            "a policy override's reason is reported",
			from:            "fwd.test",
			dns:             map[string]string{"_dmarc.fwd.test": "v=DMARC1; p=reject; rua=mailto:agg@fwd.test"},
			sourceIP:        []string{"192.0.2.5"},
			override:        "trusted_forwarder",
			overrideComment: "arc=pass",
			wantTo:          []string{"agg@fwd.test"},
			wantInReport:    []string{"<type>trusted_forwarder</type>", "<comment>arc=pass</comment>"},
		},
		{
			name:     "every rua= address gets the report",
			from:     "two.test",
			dns:      map[string]string{"_dmarc.two.test": "v=DMARC1; p=none; rua=mailto:a@two.test,mailto:b@two.test,https://two.test/r"},
			sourceIP: []string{"192.0.2.6"},
			wantTo:   []string{"a@two.test", "b@two.test"},
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
			var wantTo []string
			for _, to := range tc.wantTo {
				wantTo = append(wantTo, rename(to))
			}
			policyDomain := from
			if tc.policyDomain != "" {
				policyDomain = rename(tc.policyDomain)
			}
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
			r.reportDomains([]string{from}, time.Now())

			var queued []models.OutboundQueue
			db.Where("sender = ? AND raw_message LIKE ?", "dmarc-reports@reporter.test", "%Report Domain: "+policyDomain+" %").Order("id").Find(&queued)
			var got []string
			for _, q := range queued {
				got = append(got, q.Recipient)
			}
			if !reflect.DeepEqual(got, wantTo) {
				t.Errorf("queued reports to %q, want %q", got, wantTo)
			}
			if len(queued) > 0 && len(tc.wantInReport) > 0 {
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

// The mail of a domain and of its subdomain without a record of its own is
// one report, for the DMARC Policy Domain both use (RFC 9990 §3.1.2), with a
// record for each From domain.
func TestReporter_OneReportPerPolicyDomain(t *testing.T) {
	db := openReporterDB(t)
	policy := fmt.Sprintf("one-%d.test", time.Now().UnixNano())
	sub := "news." + policy
	t.Cleanup(func() {
		db.Where("domain IN ?", []string{policy, sub}).Delete(&models.DMARCAggregateRecord{})
		db.Where("recipient = ?", "agg@"+policy).Delete(&models.OutboundQueue{})
	})
	for _, from := range []string{policy, sub} {
		if err := db.Create(&models.DMARCAggregateRecord{
			Domain: from, SourceIP: "192.0.2.7", HeaderFrom: from, Disposition: "none", Policy: "none",
			DKIMDomain: from, DKIMSelector: "s", DKIMResult: "pass", DKIMAligned: true,
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
	r := NewReporter(db, time.Hour, "reporter.test")
	r.lookupTXT = func(name string) ([]string, error) {
		if name == "_dmarc."+policy {
			return []string{"v=DMARC1; p=none; rua=mailto:agg@" + policy}, nil
		}
		return nil, nil
	}
	r.reportDomains([]string{policy, sub}, time.Now())

	var queued []models.OutboundQueue
	db.Where("recipient = ?", "agg@"+policy).Find(&queued)
	if len(queued) != 1 {
		t.Fatalf("queued %d reports, want one", len(queued))
	}
	report := reportXML(t, queued[0].RawMessage)
	for _, want := range []string{"<header_from>" + policy + "</header_from>", "<header_from>" + sub + "</header_from>", "<selector>s</selector>"} {
		if !strings.Contains(report, want) {
			t.Errorf("report lacks %s:\n%s", want, report)
		}
	}
	if strings.Count(report, "<record>") != 2 {
		t.Errorf("want a record for each From domain:\n%s", report)
	}
}
