package apiclient

import (
	"go/build"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const modulePath = "github.com/rest-mail/rest-mail-server/"

// Clients outside this repository link the API client, so it may reach only
// other public packages of this module, never the server's internal ones.
func TestImportsOnlyPublicPackagesOfThisModule(t *testing.T) {
	pkg, err := build.ImportDir(".", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, imp := range pkg.Imports {
		if strings.HasPrefix(imp, modulePath) && !strings.HasPrefix(imp, modulePath+"pkg/") {
			t.Errorf("apiclient imports %s, which is not under pkg/", imp)
		}
	}
}

// A Doer given with WithDoer sends every call, and receives the connection's
// request ID; without one, the call goes out unwrapped.
func TestWithDoerSendsEveryCall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"address":"a@b.test","exists":true}}`))
	}))
	t.Cleanup(srv.Close)

	var calls int
	var gotID string
	do := func(client *http.Client, req *http.Request, requestID string) (*http.Response, error) {
		calls++
		gotID = requestID
		return client.Do(req)
	}
	c := New(srv.URL, WithDoer(do)).WithRequestID("conn-1")
	if _, err := c.CheckMailbox("a@b.test"); err != nil {
		t.Fatalf("CheckMailbox: %v", err)
	}
	if calls != 1 || gotID != "conn-1" {
		t.Fatalf("doer calls = %d, request ID = %q; want 1, conn-1", calls, gotID)
	}

	plain := New(srv.URL, WithDoer(nil))
	if _, err := plain.CheckMailbox("a@b.test"); err != nil {
		t.Fatalf("CheckMailbox without a doer: %v", err)
	}
	if calls != 1 {
		t.Fatalf("doer calls = %d after a client without it; want 1", calls)
	}
}
