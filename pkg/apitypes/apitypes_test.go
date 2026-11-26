package apitypes

import (
	"go/build"
	"strings"
	"testing"
)

const modulePath = "github.com/rest-mail/rest-mail-server/"

// Clients outside this repository link the wire types, so they may reach only
// other public packages of this module, never the server's internal ones.
func TestImportsOnlyPublicPackagesOfThisModule(t *testing.T) {
	pkg, err := build.ImportDir(".", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, imp := range pkg.Imports {
		if !strings.HasPrefix(imp, modulePath) {
			continue
		}
		if !strings.HasPrefix(imp, modulePath+"pkg/") {
			t.Errorf("apitypes imports %s, which is not under pkg/", imp)
		}
	}
}
