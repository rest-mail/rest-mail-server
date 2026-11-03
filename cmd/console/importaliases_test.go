package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type aliasImportFixture struct {
	rows  []importedAlias
	posts int
	get   func(http.ResponseWriter, *http.Request) bool
	post  func(http.ResponseWriter, importedAlias) bool
}

func aliasImportServer(t *testing.T, fixture *aliasImportFixture) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer private-key" {
			t.Error("missing authentication")
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			if fixture.get != nil && fixture.get(w, r) {
				return
			}
			switch r.URL.Path {
			case "/api/v1/admin/mailboxes":
				fmt.Fprint(w, `{"data":[{"address":"alice@example.test","active":true},{"address":"bob@example.test","active":true},{"address":"disabled@example.test","active":false}],"pagination":{"has_more":false}}`)
			case "/api/v1/admin/aliases":
				_ = json.NewEncoder(w).Encode(map[string]any{"data": fixture.rows, "pagination": map[string]bool{"has_more": false}})
			default:
				t.Errorf("unexpected GET %s", r.URL)
				w.WriteHeader(http.StatusNotFound)
			}
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/admin/aliases" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected mutation %s %s", r.Method, r.URL)
		}
		var row importedAlias
		if err := json.NewDecoder(r.Body).Decode(&row); err != nil {
			t.Error(err)
		}
		fixture.posts++
		if fixture.post != nil && fixture.post(w, row) {
			return
		}
		row.ID = uint(len(fixture.rows) + 1)
		row.Active = true
		fixture.rows = append(fixture.rows, row)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": row})
	}))
	t.Cleanup(server.Close)
	return server
}

func importAliasTestRun(t *testing.T, server *httptest.Server, text string, args ...string) (int, map[string][]aliasImportResult) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	flags := append([]string{"--server", server.URL, "--json"}, args...)
	code := runImportAliases(context.Background(), flags, strings.NewReader(text), "private-key", server.Client(), &stdout, &stderr)
	var result map[string][]aliasImportResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("exit %d: stdout %s stderr %s: %v", code, &stdout, &stderr, err)
	}
	if strings.Contains(stdout.String()+stderr.String(), "private-key") {
		t.Fatal("credential printed")
	}
	return code, result
}

func TestImportAliasesMapAndRerun(t *testing.T) {
	fixture := &aliasImportFixture{}
	server := aliasImportServer(t, fixture)
	text := "# Postfix / docker-mailserver virtual map\n\nSales@EXAMPLE.test alice@example.test,\n\t bob@example.test,alice@example.test\nalice@example.test bob@example.test\nbroken\nnext@example.test alice@example.test\n"
	code, result := importAliasTestRun(t, server, text)
	if code != exitFailed || len(result["created"]) != 4 || len(result["failed"]) != 1 || result["failed"][0].Line != 6 || fixture.posts != 4 {
		t.Fatalf("exit %d, posts %d, result %+v", code, fixture.posts, result)
	}
	if fixture.rows[0].SourceAddress != "sales@example.test" || fixture.rows[2].SourceAddress != "alice@example.test" {
		t.Fatalf("alias and forwarding rows: %+v", fixture.rows)
	}
	code, result = importAliasTestRun(t, server, text)
	if code != exitFailed || len(result["existing"]) != 4 || len(result["created"]) != 0 || fixture.posts != 4 {
		t.Fatalf("rerun exit %d, posts %d, result %+v", code, fixture.posts, result)
	}
}

func TestImportAliasesRefusesUnsupportedAndConflictingLines(t *testing.T) {
	fixture := &aliasImportFixture{rows: []importedAlias{
		{ID: 1, SourceAddress: "taken@example.test", DestinationAddress: "bob@example.test", Active: true},
		{ID: 2, SourceAddress: "inactive@example.test", DestinationAddress: "alice@example.test", Active: false},
	}}
	server := aliasImportServer(t, fixture)
	text := "@example.test alice@example.test\n" +
		"pipe@example.test |/bin/command\n" +
		"remote@example.test alice@example.test,Other@external.test\n" +
		"taken@example.test alice@example.test\n" +
		"inactive@example.test alice@example.test\n" +
		"disabled-forward@example.test disabled@example.test\n" +
		"loop@example.test loop@example.test\n" +
		"good@example.test bob@example.test\n"
	code, result := importAliasTestRun(t, server, text)
	if code != exitFailed || len(result["failed"]) != 7 || len(result["created"]) != 1 || fixture.posts != 1 {
		t.Fatalf("exit %d posts %d: %+v", code, fixture.posts, result)
	}
	for index, failure := range result["failed"] {
		if failure.Line != index+1 || failure.Reason == "" {
			t.Fatalf("failure %+v", failure)
		}
	}
}

func TestImportAliasesDryRunAndDefaultDomain(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runImportAliases(context.Background(), []string{"--dry-run", "--domain", "example.test", "--json"},
		strings.NewReader("sales alice,bob\ninvalid\n"), "", &http.Client{Transport: importMustNotRequest{t}}, &stdout, &stderr)
	var result map[string][]aliasImportResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if code != exitFailed || len(result["would_create"]) != 2 || len(result["failed"]) != 1 || result["would_create"][0].SourceAddress != "sales@example.test" {
		t.Fatalf("exit %d: %+v", code, result)
	}
	fixture := &aliasImportFixture{}
	server := aliasImportServer(t, fixture)
	code, result = importAliasTestRun(t, server, "sales alice,bob\n", "--domain", "example.test")
	if code != exitOK || len(result["created"]) != 2 {
		t.Fatalf("exit %d: %+v", code, result)
	}
}

func TestImportAliasesAPIRefusalsAndConcurrentDuplicate(t *testing.T) {
	fixture := &aliasImportFixture{}
	fixture.post = func(w http.ResponseWriter, row importedAlias) bool {
		switch row.SourceAddress {
		case "reserved@example.test":
			w.WriteHeader(http.StatusUnprocessableEntity)
			fmt.Fprint(w, `{"error":{"message":"private server diagnostic"}}`)
		case "forbidden@example.test":
			w.WriteHeader(http.StatusForbidden)
		case "race@example.test":
			row.ID, row.Active = 9, true
			fixture.rows = append(fixture.rows, row)
			w.WriteHeader(http.StatusConflict)
		case "conflict@example.test":
			w.WriteHeader(http.StatusConflict)
		default:
			return false
		}
		return true
	}
	server := aliasImportServer(t, fixture)
	code, result := importAliasTestRun(t, server, "reserved@example.test alice@example.test\nforbidden@example.test alice@example.test\nrace@example.test alice@example.test\nconflict@example.test alice@example.test\ngood@example.test bob@example.test\n")
	if code != exitFailed || len(result["failed"]) != 3 || len(result["existing"]) != 1 || len(result["created"]) != 1 {
		t.Fatalf("exit %d: %+v", code, result)
	}
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), "private server diagnostic") {
		t.Fatal("server diagnostic printed")
	}
}

func TestImportAliasesPaginationAndReadRefusal(t *testing.T) {
	fixture := &aliasImportFixture{}
	fixture.get = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/api/v1/admin/mailboxes" {
			return false
		}
		if r.URL.Query().Get("offset") == "0" {
			fmt.Fprint(w, `{"data":[{"address":"alice@example.test","active":true}],"pagination":{"has_more":true}}`)
		} else if r.URL.Query().Get("offset") == "1" {
			fmt.Fprint(w, `{"data":[{"address":"bob@example.test","active":true}],"pagination":{"has_more":false}}`)
		} else {
			t.Errorf("unexpected offset: %s", r.URL)
		}
		return true
	}
	server := aliasImportServer(t, fixture)
	code, result := importAliasTestRun(t, server, "sales@example.test bob@example.test\n")
	if code != exitOK || len(result["created"]) != 1 {
		t.Fatalf("paginated exit %d: %+v", code, result)
	}
	fixture.get = func(w http.ResponseWriter, r *http.Request) bool {
		w.WriteHeader(http.StatusForbidden)
		return true
	}
	code, result = importAliasTestRun(t, server, "one@example.test alice@example.test\ntwo@example.test alice@example.test\n")
	if code != exitFailed || len(result["failed"]) != 2 || fixture.posts != 1 {
		t.Fatalf("read refusal exit %d posts %d: %+v", code, fixture.posts, result)
	}
}

func TestImportAliasesFileAndUsage(t *testing.T) {
	fixture := &aliasImportFixture{}
	server := aliasImportServer(t, fixture)
	path := filepath.Join(t.TempDir(), "postfix-virtual.cf")
	if err := os.WriteFile(path, []byte("sales@example.test alice@example.test\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := runImportAliases(context.Background(), []string{"--server", server.URL, path}, strings.NewReader(""), "private-key", server.Client(), &stdout, &stderr)
	if code != exitOK || !strings.Contains(stdout.String(), "Created 1 aliases") {
		t.Fatalf("exit %d stdout %s stderr %s", code, &stdout, &stderr)
	}
	for _, args := range [][]string{{}, {"--server", server.URL, "a", "b"}, {"--server", server.URL, path + ".missing"}, {"--dry-run", "--domain", "bad domain"}, {"--unknown"}} {
		stdout.Reset()
		stderr.Reset()
		if code := runImportAliases(context.Background(), args, strings.NewReader(""), "", server.Client(), &stdout, &stderr); code != exitUsage {
			t.Fatalf("args %v: exit %d", args, code)
		}
	}
	stdout.Reset()
	stderr.Reset()
	code, handled := runSubcommand([]string{"restmail", "import-aliases", "--help"}, &stdout, &stderr)
	if !handled || code != exitOK || !strings.Contains(stdout.String(), "Postfix") {
		t.Fatalf("help: handled %v exit %d stdout %s stderr %s", handled, code, &stdout, &stderr)
	}
}

func TestImportAliasesLongLineDoesNotLoseNextLine(t *testing.T) {
	fixture := &aliasImportFixture{}
	server := aliasImportServer(t, fixture)
	code, result := importAliasTestRun(t, server, strings.Repeat("x", 65*1024)+"\ngood@example.test alice@example.test\n")
	if code != exitFailed || len(result["failed"]) != 1 || result["failed"][0].Line != 1 || len(result["created"]) != 1 {
		t.Fatalf("exit %d: %+v", code, result)
	}
}

func TestImportAliasesNeverFollowsRedirects(t *testing.T) {
	var leaked bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked = true }))
	defer target.Close()
	fixture := &aliasImportFixture{}
	fixture.post = func(w http.ResponseWriter, row importedAlias) bool {
		w.Header().Set("Location", target.URL)
		w.WriteHeader(http.StatusTemporaryRedirect)
		return true
	}
	server := aliasImportServer(t, fixture)
	code, result := importAliasTestRun(t, server, "sales@example.test alice@example.test\n")
	if code != exitFailed || len(result["failed"]) != 1 || leaked {
		t.Fatalf("exit %d leaked %v: %+v", code, leaked, result)
	}
}

func TestImportAliasesInvalidSnapshotsAndResponsesFailClosed(t *testing.T) {
	for name, body := range map[string]string{
		"malformed JSON":     `{"data":`,
		"missing pagination": `{"data":[]}`,
		"empty next page":    `{"data":[],"pagination":{"has_more":true}}`,
		"oversized response": strings.Repeat("x", (1<<20)+1),
	} {
		t.Run(name, func(t *testing.T) {
			fixture := &aliasImportFixture{}
			fixture.get = func(w http.ResponseWriter, r *http.Request) bool {
				fmt.Fprint(w, body)
				return true
			}
			server := aliasImportServer(t, fixture)
			code, result := importAliasTestRun(t, server, "sales@example.test alice@example.test\n")
			if code != exitFailed || len(result["failed"]) != 1 || fixture.posts != 0 {
				t.Fatalf("exit %d posts %d: %+v", code, fixture.posts, result)
			}
		})
	}
	for name, body := range map[string]string{
		"malformed JSON": `{"data":`,
		"wrong pair":     `{"data":{"id":1,"source_address":"wrong@example.test","destination_address":"alice@example.test","active":true}}`,
		"inactive":       `{"data":{"id":1,"source_address":"sales@example.test","destination_address":"alice@example.test","active":false}}`,
	} {
		t.Run(name, func(t *testing.T) {
			fixture := &aliasImportFixture{}
			fixture.post = func(w http.ResponseWriter, row importedAlias) bool {
				w.WriteHeader(http.StatusCreated)
				fmt.Fprint(w, body)
				return true
			}
			server := aliasImportServer(t, fixture)
			code, result := importAliasTestRun(t, server, "sales@example.test alice@example.test\n")
			if code != exitFailed || len(result["failed"]) != 1 || len(result["created"]) != 0 {
				t.Fatalf("exit %d: %+v", code, result)
			}
		})
	}
}

func TestImportAliasesInterruptedFanoutResumes(t *testing.T) {
	fixture := &aliasImportFixture{}
	refuseBob := true
	fixture.post = func(w http.ResponseWriter, row importedAlias) bool {
		if row.DestinationAddress == "bob@example.test" && refuseBob {
			w.WriteHeader(http.StatusServiceUnavailable)
			return true
		}
		return false
	}
	server := aliasImportServer(t, fixture)
	text := "team@example.test alice@example.test,bob@example.test\n"
	code, first := importAliasTestRun(t, server, text)
	if code != exitFailed || len(first["created"]) != 1 || len(first["failed"]) != 1 {
		t.Fatalf("first exit %d: %+v", code, first)
	}
	refuseBob = false
	code, second := importAliasTestRun(t, server, text)
	if code != exitOK || len(second["created"]) != 1 || len(second["existing"]) != 1 || len(fixture.rows) != 2 {
		t.Fatalf("second exit %d: %+v", code, second)
	}
}

func TestImportAliasesOversizedFileAndCanceledRequest(t *testing.T) {
	fixture := &aliasImportFixture{}
	server := aliasImportServer(t, fixture)
	var stdout, stderr bytes.Buffer
	code := runImportAliases(context.Background(), []string{"--server", server.URL}, strings.NewReader(strings.Repeat("x", (16<<20)+1)), "private-key", server.Client(), &stdout, &stderr)
	if code != exitFailed || fixture.posts != 0 || !strings.Contains(stderr.String(), "16 MiB") {
		t.Fatalf("oversized exit %d posts %d stderr %s", code, fixture.posts, &stderr)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stdout.Reset()
	stderr.Reset()
	code = runImportAliases(ctx, []string{"--server", server.URL, "--json"}, strings.NewReader("sales@example.test alice@example.test\n"), "private-key", server.Client(), &stdout, &stderr)
	var result map[string][]aliasImportResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if code != exitFailed || len(result["failed"]) != 1 || fixture.posts != 0 {
		t.Fatalf("canceled exit %d posts %d: %+v", code, fixture.posts, result)
	}
}
