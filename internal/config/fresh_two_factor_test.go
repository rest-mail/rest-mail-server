package config

import (
	"strings"
	"testing"
	"time"
)

func TestFreshTwoFactorWindow(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  time.Duration
		bad   bool
	}{
		{"", 15 * time.Minute, false}, {"5m", 5 * time.Minute, false}, {"15m", 15 * time.Minute, false},
		{"15m1s", 0, true}, {"0s", 0, true}, {"-1m", 0, true}, {"invalid", 0, true},
	} {
		t.Run(tc.value, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("ENVIRONMENT", "development")
			t.Setenv("FRESH_TWO_FACTOR_WINDOW", tc.value)
			c, err := Load()
			if tc.bad {
				if err == nil || !strings.Contains(err.Error(), "FRESH_TWO_FACTOR_WINDOW") {
					t.Fatalf("got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if c.FreshTwoFactorWindow != tc.want {
				t.Fatalf("got %v, want %v", c.FreshTwoFactorWindow, tc.want)
			}
		})
	}
}
