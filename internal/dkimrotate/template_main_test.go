package dkimrotate_test

import (
	"os"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/testdb"
)

func TestMain(tests *testing.M) {
	os.Exit(testdb.Finish(tests.Run()))
}
