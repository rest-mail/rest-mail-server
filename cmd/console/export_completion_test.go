package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// An export's exit status 0 says the account is all there (#2116 AC03). These
// tests hold that to the answers an export cannot finish on: a listing that
// says more follows without a way to reach it, and a settings read with no
// settings in it.

const sieveNotStored = `{"error":{"code":"not_found","message":"Sieve script not found"}}`

type settingsReply struct {
	status int
	body   string
}

// settingsWith is settingsAPI with some routes answering differently.
func settingsWith(t *testing.T, replies map[string]settingsReply) *httptest.Server {
	t.Helper()
	var requests []string
	upstream, err := url.Parse(settingsAPI(t, &requests).URL)
	if err != nil {
		t.Fatal(err)
	}
	rest := httputil.NewSingleHostReverseProxy(upstream)
	srv := httptest.NewServer(withExportSessions(func(w http.ResponseWriter, r *http.Request) {
		if reply, ok := replies[r.URL.Path]; ok {
			w.WriteHeader(reply.status)
			_, _ = w.Write([]byte(reply.body))
			return
		}
		rest.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestExportSettingsOfAnAccountWithoutASieveScript(t *testing.T) {
	srv := settingsWith(t, map[string]settingsReply{"/api/v1/accounts/12/sieve": {http.StatusNotFound, sieveNotStored}})
	path := filepath.Join(t.TempDir(), "settings.json")
	var out, errs bytes.Buffer
	code := runExportSettings(context.Background(), []string{"--server", srv.URL, "--account-id", "12", "--path", path, "--json"},
		settingsTestKey, srv.Client(), settingsTestTime, &out, &errs)
	if code != exitOK {
		t.Fatalf("exit %d, stderr %s", code, &errs)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got settingsExport
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, raw)
	}
	if got.Sieve != (settingsSieve{}) {
		t.Errorf("sieve %+v, want the empty script of an account without one", got.Sieve)
	}
	if got.Vacation.Subject != "Away" || got.Account.Address != "ada@example.com" {
		t.Errorf("the other sections were lost: %+v %+v", got.Vacation, got.Account)
	}
}

func TestExportSettingsRefusesAnAnswerWithoutTheSettings(t *testing.T) {
	const account = "/api/v1/accounts/12"
	null := settingsReply{http.StatusOK, `{"data":null}`}
	cases := map[string]map[string]settingsReply{
		"a proxy's 404 page for the script": {account + "/sieve": {http.StatusNotFound, "<html>not here</html>"}},
		"another 404 for the script":        {account + "/sieve": {http.StatusNotFound, `{"error":{"code":"route_missing","message":"no"}}`}},
		"the script refused":                {account + "/sieve": {http.StatusForbidden, `{"error":{"code":"forbidden","message":"no"}}`}},
		"the script read failing":           {account + "/sieve": {http.StatusInternalServerError, `{"error":{"code":"not_found","message":"no"}}`}},
		"the account gone after the script": {
			account + "/sieve":    {http.StatusNotFound, sieveNotStored},
			account + "/vacation": {http.StatusNotFound, `{"error":{"code":"not_found","message":"Account not found"}}`},
		},
		"no vacation reply answer": {account + "/vacation": {http.StatusNotFound, sieveNotStored}},
		"null account":             {account: null},
		"null folders":             {account + "/folders": null},
		"null script":              {account + "/sieve": null},
		"null vacation reply":      {account + "/vacation": null},
		"null quiet hours":         {account + "/quiet-hours": null},
		"null reply-time setting":  {account + "/contact-reply-time": null},
	}
	for name, replies := range cases {
		t.Run(name, func(t *testing.T) {
			srv := settingsWith(t, replies)
			dir := t.TempDir()
			path := filepath.Join(dir, "settings.json")
			const earlier = "the export made last week\n"
			if err := os.WriteFile(path, []byte(earlier), 0o600); err != nil {
				t.Fatal(err)
			}
			var out, errs bytes.Buffer
			code := runExportSettings(context.Background(), []string{"--server", srv.URL, "--account-id", "12", "--path", path, "--overwrite", "--json"},
				settingsTestKey, srv.Client(), settingsTestTime, &out, &errs)
			if code != exitFailed || out.Len() != 0 {
				t.Fatalf("exit %d, stdout %q, stderr %q", code, &out, &errs)
			}
			if got, _ := os.ReadFile(path); string(got) != earlier {
				t.Errorf("the earlier export was replaced by %q", got)
			}
			if entries, _ := os.ReadDir(dir); len(entries) != 1 {
				t.Errorf("a failed export left %v", entries)
			}
		})
	}
}

// exportListing serves one INBOX through the message cursor pages name, each
// keyed by the cursor that asks for it. It stops answering a client that keeps
// following the listing, so a loop fails the test rather than hanging it.
func exportListing(t *testing.T, folders string, pages map[string]string) *httptest.Server {
	t.Helper()
	var lists atomic.Int32
	srv := httptest.NewServer(withExportSessions(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/accounts/12/folders":
			_, _ = fmt.Fprint(w, folders)
		case r.URL.Path == "/api/v1/accounts/12/folders/INBOX/messages":
			if lists.Add(1) > 20 {
				t.Errorf("the export kept following the listing (cursor %q)", r.URL.Query().Get("cursor"))
				w.WriteHeader(http.StatusForbidden)
				return
			}
			page, ok := pages[r.URL.Query().Get("cursor")]
			if !ok {
				t.Errorf("unexpected cursor %q", r.URL.Query().Get("cursor"))
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = fmt.Fprint(w, page)
		case strings.HasSuffix(r.URL.Path, "/raw"):
			_, _ = fmt.Fprint(w, exportListingRaw)
		default:
			t.Errorf("unexpected request %s", r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

const (
	exportListingRaw = "Subject: listed\r\n\r\nbody\r\n"
	oneInbox         = `{"data":[{"name":"INBOX"}]}`
)

func listedPage(id int, pagination string) string {
	message := fmt.Sprintf(`{"id":%d,"raw_size":%d,"received_at":"2020-01-02T03:04:05Z"}`, id, len(exportListingRaw))
	if pagination == "" {
		return `{"data":[` + message + `]}`
	}
	return `{"data":[` + message + `],"pagination":` + pagination + `}`
}

// unreachableEnds are the listings that never show where a folder ends.
var unreachableEnds = map[string]struct {
	folders string
	pages   map[string]string
}{
	"more without a cursor": {oneInbox, map[string]string{"": listedPage(1, `{"has_more":true}`)}},
	"a cursor repeated": {oneInbox, map[string]string{
		"":  listedPage(1, `{"has_more":true,"cursor":"a"}`),
		"a": listedPage(2, `{"has_more":true,"cursor":"a"}`),
	}},
	"cursors going round": {oneInbox, map[string]string{
		"":  listedPage(1, `{"has_more":true,"cursor":"a"}`),
		"a": listedPage(2, `{"has_more":true,"cursor":"b"}`),
		"b": listedPage(3, `{"has_more":true,"cursor":"a"}`),
	}},
	"an empty page with more to follow": {oneInbox, map[string]string{
		"":  listedPage(1, `{"has_more":true,"cursor":"a"}`),
		"a": `{"data":[],"pagination":{"has_more":true,"cursor":"b"}}`,
		"b": `{"data":[],"pagination":{"has_more":false}}`,
	}},
	"more folders than the list holds": {`{"data":[{"name":"INBOX"}],"pagination":{"has_more":true,"cursor":"x"}}`,
		map[string]string{"": listedPage(1, `{"has_more":false}`)}},
}

func TestExportMaildirRefusesAListingWithoutAnEnd(t *testing.T) {
	cases := map[string]map[string]string{
		"no pagination":   {"": listedPage(1, "")},
		"null pagination": {"": listedPage(1, "null")},
	}
	run := func(t *testing.T, folders string, pages map[string]string) {
		t.Helper()
		srv := exportListing(t, folders, pages)
		code, out, errs := runDataExport(t, "export-maildir", srv.URL, filepath.Join(t.TempDir(), "maildir"))
		if code != exitFailed || out != "" || !strings.Contains(errs, "export_failed") {
			t.Fatalf("exit %d, stdout %q, stderr %q", code, out, errs)
		}
	}
	for name, pages := range cases {
		t.Run(name, func(t *testing.T) { run(t, oneInbox, pages) })
	}
	for name, listing := range unreachableEnds {
		t.Run(name, func(t *testing.T) { run(t, listing.folders, listing.pages) })
	}
}

// The control for the refusals above: the same server and messages, with a
// listing that does end.
func TestExportMaildirFinishesAListingThatEnds(t *testing.T) {
	srv := exportListing(t, oneInbox, map[string]string{
		"":  listedPage(1, `{"has_more":true,"cursor":"a"}`),
		"a": listedPage(2, `{"has_more":true,"cursor":"b"}`),
		"b": listedPage(3, `{"has_more":false}`),
	})
	path := filepath.Join(t.TempDir(), "maildir")
	code, out, errs := runDataExport(t, "export-maildir", srv.URL, path)
	if code != exitOK {
		t.Fatalf("exit %d, stderr %s", code, errs)
	}
	var summary struct{ Folders, Messages int }
	if err := json.Unmarshal([]byte(out), &summary); err != nil || summary.Folders != 1 || summary.Messages != 3 {
		t.Fatalf("summary %s (%v)", out, err)
	}
	if files, _ := os.ReadDir(filepath.Join(path, "cur")); len(files) != 3 {
		t.Fatalf("%d message files, want 3", len(files))
	}
}

func TestExportCalendarRefusesAListingWithoutAnEnd(t *testing.T) {
	for name, listing := range unreachableEnds {
		t.Run(name, func(t *testing.T) {
			srv := exportListing(t, listing.folders, listing.pages)
			dir := t.TempDir()
			code, out, errs := runDataExport(t, "export-calendar", srv.URL, filepath.Join(dir, "calendar"))
			if code != exitFailed || out != "" {
				t.Fatalf("exit %d, stdout %q, stderr %q", code, out, errs)
			}
			if entries, _ := os.ReadDir(dir); len(entries) != 0 {
				t.Errorf("a failed export left %v", entries)
			}
		})
	}
}
