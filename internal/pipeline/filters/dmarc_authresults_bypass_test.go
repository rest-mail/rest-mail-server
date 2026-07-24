package filters

import (
	"context"
	"testing"

	"github.com/restmail/restmail/internal/mime"
	"github.com/restmail/restmail/internal/pipeline"
)

// TestDMARC_ForgedInboundAuthResultsDoesNotBypass proves that an
// attacker-supplied Authentication-Results header on an inbound message can no
// longer satisfy DMARC. The message is parsed exactly as it arrives (via
// mime.Parse) so the test exercises the ingress strip, then dmarc_check runs
// with the From domain publishing p=reject and NO local SPF/DKIM/ARC verdicts.
//
// Before the fix, dmarc_check trusted the forged
// "Authentication-Results: …; spf=pass …; dkim=pass …" header and returned a
// DMARC pass; the "arc=pass" substring was a second identical bypass. After the
// fix the inbound Authentication-Results is rehomed to
// X-Original-Authentication-Results at parse time and the ARC override is taken
// only from local arc_status metadata, so each forgery is rejected.
func TestDMARC_ForgedInboundAuthResultsDoesNotBypass(t *testing.T) {
	orig := lookupDMARC
	lookupDMARC = func(string) (string, error) { return "v=DMARC1; p=reject", nil }
	defer func() { lookupDMARC = orig }()

	f := &dmarcCheckFilter{}

	cases := []struct {
		name     string
		forgedAR string
	}{
		{
			name:     "forged spf=pass + dkim=pass aligned to From domain",
			forgedAR: "x; spf=pass smtp.mailfrom=a@victim.example; dkim=pass header.d=victim.example",
		},
		{
			name:     "forged arc=pass override",
			forgedAR: "x; arc=pass",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := "From: attacker@victim.example\r\n" +
				"To: user@example.test\r\n" +
				"Subject: spoof\r\n" +
				"Authentication-Results: " + tc.forgedAR + "\r\n" +
				"\r\n" +
				"body\r\n"

			email, err := mime.Parse([]byte(raw))
			if err != nil {
				t.Fatalf("mime.Parse: %v", err)
			}

			// The forged header must not survive under the trusted key.
			if got := email.Headers.Raw["Authentication-Results"]; len(got) != 0 {
				t.Fatalf("inbound Authentication-Results still present under trusted key: %v", got)
			}
			// It is preserved (untrusted) for audit.
			if got := email.Headers.Raw["X-Original-Authentication-Results"]; len(got) == 0 {
				t.Fatalf("inbound Authentication-Results not preserved under X-Original-Authentication-Results")
			}

			res, err := f.Execute(context.Background(), email)
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if res.Action != pipeline.ActionReject {
				t.Fatalf("forged Authentication-Results bypassed DMARC p=reject: action=%s result=%s detail=%q",
					res.Action, res.Log.Result, res.Log.Detail)
			}
		})
	}
}
