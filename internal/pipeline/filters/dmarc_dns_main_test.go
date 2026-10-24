package filters

import (
	"os"
	"testing"
)

// TestMain keeps DMARC's DNS off the network: discovery, the Tree Walks of
// alignment and the existence check answer nothing, and every domain exists,
// unless a test stubs them (stubDMARC).
func TestMain(m *testing.M) {
	dmarcTXT = func(string) ([]string, error) { return nil, nil }
	dmarcExists = func(string) (bool, error) { return true, nil }
	os.Exit(m.Run())
}
