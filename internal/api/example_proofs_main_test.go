package api

import (
	"fmt"
	"os"
	"testing"

	"github.com/restmail/restmail/internal/examplepairs"
)

// TestMain writes the example pairs this package's tests proved, when CI asks for
// them (examplepairs.ProofsEnv), so a check can show each was proven (#1237).
func TestMain(m *testing.M) {
	code := m.Run()
	if err := examplepairs.WriteProofs(); err != nil {
		fmt.Fprintln(os.Stderr, "example proofs:", err)
		code = 1
	}
	os.Exit(code)
}
