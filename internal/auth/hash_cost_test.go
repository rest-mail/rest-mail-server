package auth

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func storedCost(t *testing.T, hash string, err error) int {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	cost, err := bcrypt.Cost([]byte(strings.TrimPrefix(hash, "{BLF-CRYPT}")))
	if err != nil {
		t.Fatal(err)
	}
	return cost
}

// TestShardWork_ABinaryNotBuiltByGoTestHashesAtTheProductionCost builds a real
// binary, because this test binary is the one place the cost can be lowered.
func TestShardWork_ABinaryNotBuiltByGoTestHashesAtTheProductionCost(t *testing.T) {
	out, err := exec.Command("go", "run", "./testdata/hashcost").Output()
	if err != nil {
		t.Fatalf("run the probe: %v", err)
	}
	var report struct {
		LoweringPanics   bool `json:"lowering_panics"`
		PasswordCost     int  `json:"password_cost"`
		RecoveryCodeCost int  `json:"recovery_code_cost"`
	}
	if err := json.Unmarshal(out, &report); err != nil {
		t.Fatalf("read the probe's %q: %v", out, err)
	}
	if !report.LoweringPanics {
		t.Error("UseMinimumHashCostInTests did not panic outside a test binary")
	}
	// Passwords hash at 12 (#1642); recovery codes stay at 10 (recoveryCodeHashCost).
	if report.PasswordCost != 12 || report.RecoveryCodeCost != 10 {
		t.Errorf("password cost %d and recovery code cost %d, want 12 and 10", report.PasswordCost, report.RecoveryCodeCost)
	}
}

// This package's tests never call UseMinimumHashCostInTests except below, which
// restores it: they see what production sees.
func TestShardWork_TheCostIsTheProductionOneUntilATestLowersIt(t *testing.T) {
	password, err := HashPassword("password")
	if got := storedCost(t, password, err); got != 12 {
		t.Errorf("password cost = %d, want 12", got)
	}
	code, err := HashRecoveryCode("abcd-efgh")
	if got := storedCost(t, code, err); got != 10 {
		t.Errorf("recovery code cost = %d, want 10", got)
	}
	if got := storedCost(t, DummyPasswordHash, nil); got != 12 {
		t.Errorf("DummyPasswordHash cost = %d, want the cost of a real password hash, 12", got)
	}

	UseMinimumHashCostInTests()
	t.Cleanup(func() { minimumHashCostInTests.Store(false) })
	lowered, err := HashPassword("password")
	if got := storedCost(t, lowered, err); got != bcrypt.MinCost {
		t.Errorf("lowered password cost = %d, want %d", got, bcrypt.MinCost)
	}
	if err := CheckPassword("password", lowered); err != nil {
		t.Errorf("a lowered hash does not verify: %v", err)
	}
	if err := CheckPassword("other", lowered); err == nil {
		t.Error("a lowered hash accepted the wrong password")
	}
}
