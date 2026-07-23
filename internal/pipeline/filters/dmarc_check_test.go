package filters

import (
	"context"
	"testing"

	"github.com/restmail/restmail/internal/pipeline"
)

// emailWithAR builds a From-example.test message carrying the given
// Authentication-Results header (as the SPF/DKIM filters would leave it).
func emailWithAR(ar string) *pipeline.EmailJSON {
	return &pipeline.EmailJSON{
		Headers: pipeline.Headers{
			From: []pipeline.Address{{Address: "user@example.test"}},
			Raw:  map[string][]string{"Authentication-Results": {ar}},
		},
	}
}

func TestDMARC_DKIMAlignment(t *testing.T) {
	orig := lookupDMARC
	lookupDMARC = func(string) (string, error) { return "v=DMARC1; p=reject", nil }
	defer func() { lookupDMARC = orig }()

	f := &dmarcCheckFilter{}

	cases := []struct {
		name       string
		ar         string
		wantAction pipeline.Action
		wantResult string
	}{
		{
			name:       "aligned dkim passes DMARC",
			ar:         "restmail; dkim=pass header.d=example.test",
			wantAction: pipeline.ActionContinue,
			wantResult: "pass",
		},
		{
			// The hardening: a DKIM pass whose signing domain isn't aligned must
			// NOT satisfy DMARC (previously any dkim=pass was assumed aligned).
			name:       "unaligned dkim domain is rejected under p=reject",
			ar:         "restmail; dkim=pass header.d=evil.test",
			wantAction: pipeline.ActionReject,
			wantResult: "fail",
		},
		{
			// A dkim=pass with no identifiable signing domain no longer counts as
			// aligned (the old stub-era shortcut). The real verifier always emits
			// header.d= on pass, so this only affects spoofed/degenerate input.
			name:       "dkim pass without header.d is not aligned",
			ar:         "restmail; dkim=pass",
			wantAction: pipeline.ActionReject,
			wantResult: "fail",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := f.Execute(context.Background(), emailWithAR(tc.ar))
			if err != nil {
				t.Fatal(err)
			}
			if res.Action != tc.wantAction {
				t.Errorf("action: got %s want %s (detail: %s)", res.Action, tc.wantAction, res.Log.Detail)
			}
			if res.Log.Result != tc.wantResult {
				t.Errorf("result: got %s want %s", res.Log.Result, tc.wantResult)
			}
		})
	}
}

func TestDMARC_SPFAlignmentStillPasses(t *testing.T) {
	orig := lookupDMARC
	lookupDMARC = func(string) (string, error) { return "v=DMARC1; p=reject", nil }
	defer func() { lookupDMARC = orig }()

	f := &dmarcCheckFilter{}
	// SPF-aligned alone satisfies DMARC even with no DKIM.
	res, err := f.Execute(context.Background(), emailWithAR("spf=pass smtp.mailfrom=user@example.test"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Action != pipeline.ActionContinue || res.Log.Result != "pass" {
		t.Errorf("SPF-aligned should pass DMARC, got action=%s result=%s", res.Action, res.Log.Result)
	}
}
