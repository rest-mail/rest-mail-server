package config

import "testing"

func TestPersistentSendLimitDefaultsAndStrictConfiguration(t *testing.T) {
	cfg, err := loadSendLimits(123, 20)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Account.MessagesPerHour != 123 || cfg.Account.MessagesPerDay != 1000 || cfg.Account.RecipientsPerHour != 1000 || cfg.Account.RecipientsPerDay != 10000 || cfg.PerMinute != 20 {
		t.Fatalf("defaults: %+v", cfg)
	}
	t.Setenv("SEND_LIMIT_MESSAGES_PER_HOUR", "7")
	t.Setenv("DOMAIN_SEND_LIMIT_RECIPIENTS_PER_DAY", "40")
	t.Setenv("SERVER_SEND_LIMIT_MESSAGES_PER_DAY", "60")
	cfg, err = loadSendLimits(123, 20)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Account.MessagesPerHour != 7 || cfg.Domain.RecipientsPerDay != 40 || cfg.Server.MessagesPerDay != 60 {
		t.Fatalf("configured budgets: %+v", cfg)
	}
	for _, bad := range []string{"-1", "nope", "9223372036854775808"} {
		t.Setenv("SEND_LIMIT_RECIPIENTS_PER_HOUR", bad)
		if _, err := loadSendLimits(100, 20); err == nil {
			t.Fatalf("accepted bad ceiling %q", bad)
		}
	}
}
