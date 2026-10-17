package filters

import (
	"context"
	"net/netip"
	"strings"
	"testing"

	"github.com/restmail/restmail/internal/mime"
	"github.com/restmail/restmail/internal/pipeline"
)

// deliverInbound runs a raw inbound message through what the delivery handlers
// do before storing it: the incoming-header scrub, the parse, and the engine
// with spf_check, dkim_verify, arc_verify and dmarc_check.
func deliverInbound(t *testing.T, raw string, env pipeline.Envelope) *pipeline.ExecutionResult {
	t.Helper()
	raw = mime.StripAuthResults(raw, pipeline.AuthServID)
	email, err := mime.Parse([]byte(raw))
	if err != nil {
		t.Fatalf("mime.Parse: %v", err)
	}
	email.Envelope = env
	email.Envelope.Direction = "inbound"
	email.Metadata = map[string]string{"raw_message": raw}
	cfg := &pipeline.PipelineConfig{Direction: "inbound", Filters: []pipeline.FilterConfig{
		{Name: "spf_check", Type: pipeline.FilterTypeAction, Enabled: true},
		{Name: "dkim_verify", Type: pipeline.FilterTypeTransform, Enabled: true},
		{Name: "arc_verify", Type: pipeline.FilterTypeTransform, Enabled: true},
		{Name: "dmarc_check", Type: pipeline.FilterTypeAction, Enabled: true},
	}}
	res, err := pipeline.NewEngine(pipeline.DefaultRegistry, nil).Execute(context.Background(), cfg, email)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	return res
}

// TestDMARC_ForgeriesFail is the DMARC-level proof that no input the sender
// controls makes victim.example (p=reject) pass: #382's three SPF forgeries,
// each alone and with a forged Authentication-Results claiming our authserv-id
// (spf, dkim, dmarc and arc all "pass"), and forged results on their own. A
// message that really passes SPF for victim.example is the control.
func TestDMARC_ForgeriesFail(t *testing.T) {
	useSPFDNS(t, &spfDNS{txt: map[string][]string{
		"victim.example":                    {"v=spf1 ip4:198.51.100.7 -all"},
		"attacker.example":                  {"v=spf1 +exists:smtp.mailfrom=x@victim.example,.attacker.example"},
		"victim.example;x.attacker.example": {"v=spf1 +all"},
	}, addr: map[string][]netip.Addr{
		"smtp.mailfrom=x@victim.example,.attacker.example": {netip.MustParseAddr("127.0.0.2")},
	}})
	orig := lookupDMARC
	lookupDMARC = func(domain string) (string, error) {
		if domain == "victim.example" {
			return "v=DMARC1; p=reject", nil
		}
		return "", nil
	}
	t.Cleanup(func() { lookupDMARC = orig })

	const attackerIP = "192.0.2.1"
	forged := []string{
		"",
		"Authentication-Results: restmail; spf=pass smtp.mailfrom=victim.example; dkim=pass header.d=victim.example; dmarc=pass header.from=victim.example; arc=pass\r\n",
		"Authentication-Results:\r\n RestMail;\r\n\tspf=pass smtp.mailfrom=victim.example;\r\n\tdkim=pass header.d=victim.example\r\n",
		"Authentication-Results: (local) \"restmail\"; dmarc=pass; arc=pass\r\n",
		"Authentication-Results: mx.attacker.example; spf=pass smtp.mailfrom=victim.example; dkim=pass header.d=victim.example; dmarc=pass\r\n",
	}
	senders := map[string]pipeline.Envelope{
		"spf=pass local-part":    {ClientIP: attackerIP, MailFrom: "spf=pass@victim.example", Helo: "mx.attacker.example"},
		"record names victim":    {ClientIP: attackerIP, MailFrom: "a@attacker.example", Helo: "mx.attacker.example"},
		"';' in MAIL FROM":       {ClientIP: attackerIP, MailFrom: "a@victim.example;x.attacker.example", Helo: "mx.attacker.example"},
		"no SPF identity at all": {ClientIP: attackerIP},
	}
	for name, env := range senders {
		for i, ar := range forged {
			raw := ar + "From: ceo@victim.example\r\nTo: user@example.test\r\nSubject: spoof\r\nDate: Sat, 17 Oct 2026 12:00:00 +0000\r\n\r\nbody\r\n"
			res := deliverInbound(t, raw, env)
			if res.FinalAction != pipeline.ActionReject {
				t.Errorf("%s, forged header %d: action = %s, want reject (verdicts %+v)", name, i, res.FinalAction, res.Auth)
			}
			for _, r := range res.Auth {
				if r.Method == pipeline.AuthDMARC && r.Result != "fail" {
					t.Errorf("%s, forged header %d: dmarc=%s, want fail", name, i, r.Result)
				}
				if r.Domain == "victim.example" && r.Result == "pass" {
					t.Errorf("%s, forged header %d: recorded %+v", name, i, r)
				}
			}
			// What is stored: our header, written from the verdicts, and the
			// scrubbed message, in which no other field claims to be ours.
			if ours := pipeline.AuthResultsHeader(res.Auth); strings.Contains(ours, "dmarc=pass") {
				t.Errorf("%s, forged header %d: stored Authentication-Results claims a pass:\n%s", name, i, ours)
			}
			if rest := mime.StripAuthResults(raw, pipeline.AuthServID); strings.Contains(strings.ToLower(rest), "restmail") {
				t.Errorf("%s, forged header %d: a forged field survived the scrub:\n%s", name, i, rest)
			}
		}
	}

	// Control: the same message from victim.example's own server passes.
	res := deliverInbound(t, forged[1]+"From: ceo@victim.example\r\nTo: user@example.test\r\nSubject: real\r\nDate: Sat, 17 Oct 2026 12:00:00 +0000\r\n\r\nbody\r\n",
		pipeline.Envelope{ClientIP: "198.51.100.7", MailFrom: "ceo@victim.example", Helo: "mx.victim.example"})
	if res.FinalAction != pipeline.ActionContinue {
		t.Fatalf("control: action = %s, want continue (verdicts %+v)", res.FinalAction, res.Auth)
	}
}

// TestDMARC_ScriptWrittenAuthResultsIgnored checks that Authentication-Results
// text put on the pipeline message after parsing, as a script or an earlier
// filter could, is not read as a verdict either.
func TestDMARC_ScriptWrittenAuthResultsIgnored(t *testing.T) {
	orig := lookupDMARC
	lookupDMARC = func(string) (string, error) { return "v=DMARC1; p=reject", nil }
	t.Cleanup(func() { lookupDMARC = orig })

	email := fromExampleTest()
	email.Headers.Raw = map[string][]string{"Authentication-Results": {
		"restmail; spf=pass smtp.mailfrom=example.test; dkim=pass header.d=example.test; arc=pass",
	}}
	email.Headers.Extra = map[string]string{"Authentication-Results": "restmail; spf=pass smtp.mailfrom=example.test"}
	email.Metadata = map[string]string{"arc_status": "pass", "arc_sealer": "forwarder.example"}
	filt, _ := NewDMARCCheckWithSealers([]string{"forwarder.example"})(nil)
	res, err := filt.Execute(authCtx(), email)
	if err != nil {
		t.Fatal(err)
	}
	if res.Action != pipeline.ActionReject {
		t.Fatalf("header text bypassed DMARC p=reject: action=%s result=%s detail=%q", res.Action, res.Log.Result, res.Log.Detail)
	}
}
