package config

import (
	"strings"
	"testing"
)

func TestValidateMasterKey(t *testing.T) {
	floor := strings.Repeat("k", MinMasterKeyLength)
	cases := []struct {
		name, key, wantErr string
	}{
		{"empty", "", "MASTER_KEY is empty"},
		{"short", "short", "MASTER_KEY is too short (5 bytes)"},
		{"one under the floor", floor[1:], "MASTER_KEY is too short"},
		{"at the floor", floor, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateMasterKey(tc.key)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateMasterKey refused a key at the floor: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("ValidateMasterKey = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

// The server's own check must say what ValidateMasterKey says: rotate-key calls
// that function on the assumption that the server refuses the same keys.
func TestSecurityConfigUsesValidateMasterKey(t *testing.T) {
	for _, key := range []string{"", "short"} {
		want := ValidateMasterKey(key)
		err := (&Config{Environment: "production", MasterKey: key}).validateSecurityConfig()
		if err == nil || !strings.Contains(err.Error(), want.Error()) {
			t.Fatalf("validateSecurityConfig with MASTER_KEY %q = %v, want it to contain %q", key, err, want)
		}
	}
}
