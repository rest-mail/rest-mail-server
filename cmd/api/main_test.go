package main

import (
	"os"
	"sync"
	"testing"

	dkim "github.com/rest-mail/go-dkim/pkg/dkim"
	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/outbound"
)

func TestMain(m *testing.M) {
	// Nothing here checks a hash's cost; internal/auth proves the cost production
	// uses. This also covers the operator subprocesses, which are this binary.
	auth.UseMinimumHashCostInTests()
	os.Exit(m.Run())
}

var sharedDKIMKeyPair = sync.OnceValues(func() ([2]string, error) {
	private, public, err := dkim.GenerateKey(outbound.MinDKIMRSABits)
	return [2]string{private, public}, err
})

// sharedDKIMKey is one real DKIM key for the whole package. RSA generation is the
// slow part of these fixtures and no test needs a key unlike any other test's.
func sharedDKIMKey(t *testing.T) (private, public string) {
	t.Helper()
	pair, err := sharedDKIMKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	return pair[0], pair[1]
}
