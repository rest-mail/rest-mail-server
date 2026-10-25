package queue

import (
	"bytes"
	"net"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/restmail/restmail/internal/dane"
	"github.com/restmail/restmail/internal/db/models"
)

// A DANE session is reported with policy type tlsa and its TLSA records as the policy
// strings (RFC 8460 §4.2): an authenticated session as a success, a certificate no
// record matches as tlsa-invalid, and TLSA records that failed DNSSEC validation as
// dnssec-invalid (§4.3).
func TestDANESessionsAreReported(t *testing.T) {
	db := openTLSReportDB(t)
	cert, _ := relayTestCertificate(t, false)
	good := []dane.TLSA{eeRecordFor(t, cert.Certificate[0])}
	wrong := []dane.TLSA{{Usage: dane.UsageDANEEE, Selector: 1, MatchingType: 1, Data: bytes.Repeat([]byte{7}, 32)}}

	for _, records := range [][]dane.TLSA{good, wrong} {
		settings, finish := relayTestPeer(t, cert, relayPeerOptions{noAUTH: true})
		w := daneTestWorker()
		w.db = db
		_ = w.deliverSMTPWith(net.JoinHostPort(settings.host, strconv.Itoa(settings.port)), settings.host, tlsTestItem(), nil, nil, records)
		finish()
	}
	w := daneTestWorker()
	w.db = db
	w.dane = fakeDANE{tlsaSec: dane.Bogus}
	if _, err := w.daneRecords("example.test", "mx.example.test", true); err == nil {
		t.Fatal("a bogus TLSA lookup did not defer")
	}

	var sessions []models.TLSRPTSession
	if err := db.Where("policy_type = ?", tlsrptPolicyTLSA).Find(&sessions).Error; err != nil {
		t.Fatal(err)
	}
	var results []string
	for _, s := range sessions {
		results = append(results, s.ResultType)
		if s.PolicyDomain != "example.test" {
			t.Errorf("policy domain %q", s.PolicyDomain)
		}
		if s.ResultType != tlsrptDNSSECInvalid && !strings.Contains(s.PolicyString, `"3 1 1 `) {
			t.Errorf("policy string %s does not list the TLSA record", s.PolicyString)
		}
	}
	sort.Strings(results)
	if strings.Join(results, ",") != ","+tlsrptDNSSECInvalid+","+tlsrptTLSAInvalid {
		t.Fatalf("result types %q, want a success, dnssec-invalid and tlsa-invalid", results)
	}
}

func TestBuildTLSReportForDANE(t *testing.T) {
	policy := `["3 1 1 AABB"]`
	sessions := []models.TLSRPTSession{
		{Day: "2026-10-24", PolicyDomain: "example.test", PolicyType: tlsrptPolicyTLSA, PolicyString: policy, ReceivingMX: "mx.example.test"},
		{Day: "2026-10-24", PolicyDomain: "example.test", PolicyType: tlsrptPolicyTLSA, PolicyString: policy, ReceivingMX: "mx.example.test",
			ResultType: tlsrptTLSAInvalid, FailureReason: "dane: no TLSA record matches"},
	}
	report, err := buildTLSReport("2026-10-24", "example.test", "sender.test", sessions)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Policies) != 1 {
		t.Fatalf("%d policies, want one tlsa policy", len(report.Policies))
	}
	p := report.Policies[0]
	if p.Policy.PolicyType != "tlsa" || len(p.Policy.PolicyString) != 1 || p.Policy.PolicyString[0] != "3 1 1 AABB" {
		t.Errorf("policy %+v", p.Policy)
	}
	if p.Summary.Successful != 1 || p.Summary.Failed != 1 || len(p.FailureDetails) != 1 || p.FailureDetails[0].ResultType != "tlsa-invalid" {
		t.Errorf("summary %+v, details %+v", p.Summary, p.FailureDetails)
	}
}
