package config

import "testing"

func TestFilterRequestCaps(t *testing.T) {
	clearEnv(t)
	c := &Config{SMTPMaxMessageSize: 4 << 20}
	if got, want := c.WebhookMaxRequestBytes(), int64(8<<20); got != want {
		t.Errorf("webhook default = %d, want twice the message size %d", got, want)
	}
	if got := (&Config{}).WebhookMaxRequestBytes(); got != DefaultSMTPMaxMessageSize*WebhookRequestExpansionFactor {
		t.Errorf("webhook default with no message size = %d", got)
	}
	if c.RspamdMaxRequestBytes() != 50<<20 || c.ClamAVMaxRequestBytes() != 25<<20 {
		t.Errorf("scanner defaults = %d, %d", c.RspamdMaxRequestBytes(), c.ClamAVMaxRequestBytes())
	}
	t.Setenv("WEBHOOK_MAX_REQUEST_BYTES", "1000")
	t.Setenv("RSPAMD_MAX_REQUEST_BYTES", " 2000 ")
	t.Setenv("CLAMAV_MAX_REQUEST_BYTES", "3000")
	if c.WebhookMaxRequestBytes() != 1000 || c.RspamdMaxRequestBytes() != 2000 || c.ClamAVMaxRequestBytes() != 3000 {
		t.Errorf("overrides = %d, %d, %d", c.WebhookMaxRequestBytes(), c.RspamdMaxRequestBytes(), c.ClamAVMaxRequestBytes())
	}
	for _, bad := range []string{"0", "-5", "abc", "1.5e3"} {
		t.Setenv("CLAMAV_MAX_REQUEST_BYTES", bad)
		if c.ClamAVMaxRequestBytes() != 25<<20 {
			t.Errorf("CLAMAV_MAX_REQUEST_BYTES=%q gave %d, want the default", bad, c.ClamAVMaxRequestBytes())
		}
	}
}
