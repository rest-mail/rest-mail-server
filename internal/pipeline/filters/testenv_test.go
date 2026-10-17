package filters

import (
	"os"
	"strconv"
)

// fcEnvOr and fcEnvIntOr read the unit-test Postgres settings for this
// package's DB tests, falling back to the repo defaults.
func fcEnvOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func fcEnvIntOr(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
