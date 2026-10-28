package config

import "testing"

func TestMaxOwnedAccounts(t *testing.T) {
	for _, tc := range []struct {
		value   string
		want    int
		invalid bool
	}{
		{"", 0, false}, {"0", 0, false}, {"3", 3, false}, {"-1", 0, true}, {"many", 0, true},
	} {
		t.Run(tc.value, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("ENVIRONMENT", "test")
			if tc.value != "" {
				t.Setenv("MAX_OWNED_ACCOUNTS", tc.value)
			}
			cfg, err := Load()
			if tc.invalid {
				if err == nil {
					t.Fatal("expected invalid account count")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.MaxOwnedAccounts != tc.want {
				t.Fatalf("got %d, want %d", cfg.MaxOwnedAccounts, tc.want)
			}
		})
	}
}
