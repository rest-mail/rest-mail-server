package filters

import (
	"net"
	"strings"
	"testing"

	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/pipeline"
)

// stubExists makes the existence check say exists(domain), for the test.
func stubExists(t *testing.T, exists func(string) bool) {
	t.Helper()
	orig := dmarcExists
	dmarcExists = func(domain string) (bool, error) { return exists(domain), nil }
	t.Cleanup(func() { dmarcExists = orig })
}

// A subdomain that does not exist gets its Organizational Domain's np=, one
// that exists its sp= (RFC 9989 §4.10.1).
func TestDMARC_NonExistentSubdomainPolicy(t *testing.T) {
	stubDMARC(t, onlyAt("v=DMARC1; p=none; sp=quarantine; np=reject", "example.test"))
	stubExists(t, func(domain string) bool { return domain != "nx.example.test" })
	f := &dmarcCheckFilter{}
	for from, want := range map[string]pipeline.Action{
		"ceo@nx.example.test":   pipeline.ActionReject,
		"ceo@mail.example.test": pipeline.ActionQuarantine,
		"ceo@example.test":      pipeline.ActionContinue,
	} {
		res, err := f.Execute(authCtx(spfVerdict("pass", "attacker.test")), fromAddress(from))
		if err != nil {
			t.Fatal(err)
		}
		if res.Action != want {
			t.Errorf("%s: action %s, want %s (%s)", from, res.Action, want, res.Log.Detail)
		}
	}
}

// In test mode (t=y) the policy is applied one level lower, and the reason
// is given (RFC 9989 §4.7, RFC 9990 §3.1.6).
func TestDMARC_TestModeAppliesOneLevelLower(t *testing.T) {
	f := &dmarcCheckFilter{}
	for record, want := range map[string]pipeline.Action{
		"v=DMARC1; p=reject; t=y":     pipeline.ActionQuarantine,
		"v=DMARC1; p=quarantine; t=y": pipeline.ActionContinue,
		"v=DMARC1; p=reject; t=n":     pipeline.ActionReject,
	} {
		stubDMARC(t, onlyAt(record, "example.test"))
		res, err := f.Execute(authCtx(spfVerdict("pass", "attacker.test")), fromExampleTest())
		if err != nil {
			t.Fatal(err)
		}
		if res.Action != want || res.Log.Result != "fail" {
			t.Errorf("%q: %s/%s, want %s/fail (%s)", record, res.Action, res.Log.Result, want, res.Log.Detail)
		}
		if strings.Contains(record, "t=y") && !strings.Contains(res.Log.Detail, "policy_test_mode") {
			t.Errorf("%q: the override is not given: %s", record, res.Log.Detail)
		}
	}
}

// Mail that fails p=reject but came through a mailing list is quarantined,
// not rejected, and the override is given (RFC 9989 §5.4, §7.4).
func TestDMARC_MailingListQuarantinedNotRejected(t *testing.T) {
	stubDMARC(t, onlyAt("v=DMARC1; p=reject", "example.test"))
	f := &dmarcCheckFilter{}
	email := fromExampleTest()
	email.Headers.Raw = map[string][]string{"List-Id": {" Friends <friends.lists.example.org>"}}
	res, err := f.Execute(authCtx(dkimVerdict("pass", "lists.example.org")), email)
	if err != nil {
		t.Fatal(err)
	}
	if res.Action != pipeline.ActionQuarantine || !strings.Contains(res.Log.Detail, "mailing_list: List-Id: Friends <friends.lists.example.org>") {
		t.Errorf("list mail: %s (%s), want quarantine with the mailing_list reason", res.Action, res.Log.Detail)
	}
	res, err = f.Execute(authCtx(dkimVerdict("pass", "lists.example.org")), fromExampleTest())
	if err != nil {
		t.Fatal(err)
	}
	if res.Action != pipeline.ActionReject {
		t.Errorf("mail with no List-Id: %s, want reject", res.Action)
	}
}

// A DNS failure that keeps an identifier from being judged aligned leaves
// the message neither passing nor failing: dmarc=temperror, and the policy
// is not applied (RFC 9989 §5.3.6).
func TestDMARC_AlignmentDNSFailureIsTemperror(t *testing.T) {
	temp := &net.DNSError{Err: "server misbehaving", IsTemporary: true}
	orig := dmarcTXT
	dmarcTXT = func(name string) ([]string, error) {
		switch name {
		case "_dmarc.example.test":
			return []string{"v=DMARC1; p=reject"}, nil
		case "_dmarc.bounce.example.test":
			return nil, temp
		}
		return nil, nil
	}
	t.Cleanup(func() { dmarcTXT = orig })
	ctx := authCtx(spfVerdict("pass", "bounce.example.test"))
	res, err := (&dmarcCheckFilter{}).Execute(ctx, fromExampleTest())
	if err != nil {
		t.Fatal(err)
	}
	if res.Action != pipeline.ActionContinue || res.Log.Result != "temperror" {
		t.Errorf("got %s/%s, want continue/temperror (%s)", res.Action, res.Log.Result, res.Log.Detail)
	}
	if got := pipeline.AuthResultsFromContext(ctx).Find(pipeline.AuthDMARC, pipeline.PropHeaderFrom); len(got) != 1 || got[0].Result != "temperror" {
		t.Errorf("DMARC verdict %+v, want temperror", got)
	}
}

// The aggregate row names what was authenticated: the signature's d= and s=,
// the SPF-checked domain, the recipient's domain, and the reason a policy
// was departed from (RFC 9990 §3.1.1.10 to §3.1.1.13, §3.1.6).
func TestDMARC_CaptureRecordsWhatWasAuthenticated(t *testing.T) {
	db, domain := openDMARCCaptureDB(t)
	stubDMARC(t, onlyAt("v=DMARC1; p=reject; t=y; rua=mailto:agg@"+domain, domain))
	email := fromAddress("user@" + domain)
	email.Envelope.Direction = "inbound"
	email.Envelope.ClientIP = "192.0.2.30"
	email.Envelope.RcptTo = []string{"dana@receiver.test"}
	dkim := dkimVerdict("pass", "esp.test")
	dkim.Selector = "s1"
	if _, err := (&dmarcCheckFilter{}).Execute(pipeline.WithDB(authCtx(dkim, spfVerdict("softfail", "bounce.esp.test")), db), email); err != nil {
		t.Fatal(err)
	}
	var row models.DMARCAggregateRecord
	if err := db.Where("domain = ?", domain).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.DKIMDomain != "esp.test" || row.DKIMSelector != "s1" || row.DKIMResult != "pass" || row.DKIMAligned ||
		row.SPFDomain != "bounce.esp.test" || row.SPFResult != "softfail" || row.EnvelopeTo != "receiver.test" ||
		row.Policy != "reject" || row.Disposition != "quarantine" || row.OverrideReason != "policy_test_mode" {
		t.Errorf("captured %+v", row)
	}
}
