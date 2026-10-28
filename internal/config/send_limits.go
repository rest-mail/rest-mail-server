package config

import "fmt"

// SendLimitDefaults are persistent fixed UTC hour/day submission budgets. Zero
// disables a dimension. Account defaults may be overridden by a 2FA session;
// domain and server budgets remain ceilings above every account and release.
type SendLimitDefaults struct {
	MessagesPerHour   int64
	MessagesPerDay    int64
	RecipientsPerHour int64
	RecipientsPerDay  int64
}
type SendLimitConfig struct {
	Account   SendLimitDefaults
	Domain    SendLimitDefaults
	Server    SendLimitDefaults
	PerMinute int64
}

func loadSendLimits(hour, minute int) (*SendLimitConfig, error) {
	// Preserve the older API_SEND_RATE_LIMIT knobs' nonpositive=disabled rule.
	if hour < 0 {
		hour = 0
	}
	if minute < 0 {
		minute = 0
	}
	cfg := &SendLimitConfig{PerMinute: int64(minute)}
	for _, group := range []struct {
		prefix   string
		value    *SendLimitDefaults
		defaults [4]int64
	}{
		{"SEND_LIMIT", &cfg.Account, [4]int64{int64(hour), 1000, 1000, 10000}},
		{"DOMAIN_SEND_LIMIT", &cfg.Domain, [4]int64{}},
		{"SERVER_SEND_LIMIT", &cfg.Server, [4]int64{}},
	} {
		names := [4]string{"MESSAGES_PER_HOUR", "MESSAGES_PER_DAY", "RECIPIENTS_PER_HOUR", "RECIPIENTS_PER_DAY"}
		dest := [4]*int64{&group.value.MessagesPerHour, &group.value.MessagesPerDay, &group.value.RecipientsPerHour, &group.value.RecipientsPerDay}
		for i, n := range names {
			key := group.prefix + "_" + n
			v, err := getEnvInt64Strict(key, group.defaults[i], "sends")
			if err != nil {
				return nil, err
			}
			if v < 0 {
				return nil, fmt.Errorf("%s must be nonnegative", key)
			}
			*dest[i] = v
		}
	}
	return cfg, nil
}
