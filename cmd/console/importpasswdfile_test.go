package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// A Dovecot passwd-file as an install moving to REST Mail holds it: a comment,
// a blank line, users with and without a domain, the schemes REST Mail
// imports, and lines it must refuse without stopping.
const passwdFile = `# users of example.test
alice@example.test:{SHA512-CRYPT}` + cliImportHash + `:1000:1000::/home/alice::

bob:{BLF-CRYPT}$2y$05$abcdefghijklmnopqrstuuvwxyz0123456789ABCDEFGHIJKLMNOP:1001:1001:Bob:/home/bob
carol@example.test:{ARGON2ID}$argon2id$v=19$m=4096,t=2,p=1$aW1wb3J0c2FsdDAxMjM0NQ$JCLzuH2QfsNfwyNUzLd3IjMXXY7Rb46hqjBt5hs545I
dave@example.test:{MD5-CRYPT}$1$salt$checksumchecksumchecksu
erin@example.test::1004:1004
existing@example.test:{SHA512-CRYPT}` + cliImportHash + `
`

type recordedImport struct{ address, hash string }

func passwdFileServer(t *testing.T) (*httptest.Server, *[]recordedImport) {
	t.Helper()
	var got []recordedImport
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/accounts" || r.Header.Get("Authorization") != "Bearer private-key" {
			t.Error("wrong request method, path or authentication")
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		got = append(got, recordedImport{body["address"], body["password_hash"]})
		if body["address"] == "existing@example.test" {
			w.WriteHeader(http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusCreated)
		// Even a server echoing secrets must not make the CLI print them.
		_, _ = w.Write([]byte(`{"data":{"id":` + strconv.Itoa(len(got)) + `},"echo":"` + body["password_hash"] + `"}`))
	}))
	t.Cleanup(s.Close)
	return s, &got
}

func TestImportPasswdFile(t *testing.T) {
	s, got := passwdFileServer(t)
	var out, errs bytes.Buffer
	code := runImportPasswdFile(context.Background(), []string{"--server", s.URL, "--domain", "example.test", "--json"},
		strings.NewReader(passwdFile), "private-key", s.Client(), &out, &errs)
	if code != exitFailed {
		t.Fatalf("exit %d: two lines fail, so the import must say so; stderr %s", code, &errs)
	}
	want := []recordedImport{
		{"alice@example.test", "{SHA512-CRYPT}" + cliImportHash},
		{"bob@example.test", "{BLF-CRYPT}$2y$05$abcdefghijklmnopqrstuuvwxyz0123456789ABCDEFGHIJKLMNOP"},
		{"carol@example.test", "{ARGON2ID}$argon2id$v=19$m=4096,t=2,p=1$aW1wb3J0c2FsdDAxMjM0NQ$JCLzuH2QfsNfwyNUzLd3IjMXXY7Rb46hqjBt5hs545I"},
		{"existing@example.test", "{SHA512-CRYPT}" + cliImportHash},
	}
	if len(*got) != len(want) {
		t.Fatalf("sent %d imports, want %d: %v", len(*got), len(want), *got)
	}
	for i := range want {
		if (*got)[i] != want[i] {
			t.Errorf("import %d: got %v, want %v", i, (*got)[i], want[i])
		}
	}

	var outcome map[string][]passwdFileResult
	if err := json.Unmarshal(out.Bytes(), &outcome); err != nil {
		t.Fatalf("not one JSON object: %v\n%s", err, &out)
	}
	addresses := func(kind string) (list []string) {
		for _, r := range outcome[kind] {
			list = append(list, r.Address)
		}
		return
	}
	if a := strings.Join(addresses("created"), " "); a != "alice@example.test bob@example.test carol@example.test" {
		t.Errorf("created: %s", a)
	}
	if a := strings.Join(addresses("existing"), " "); a != "existing@example.test" {
		t.Errorf("existing: %s", a)
	}
	failed := outcome["failed"]
	if len(failed) != 2 || failed[0].Address != "dave@example.test" || failed[0].Line != 6 ||
		failed[1].Address != "erin@example.test" || failed[1].Reason != "no password in the file" {
		t.Errorf("failed: %+v", failed)
	}
	for _, secret := range []string{"importsalt", "aW1wb3J0c2Fsd", "abcdefghijklmnop", "private-key", "checksum"} {
		if strings.Contains(out.String()+errs.String(), secret) {
			t.Fatalf("the import printed a secret (%s)", secret)
		}
	}
}

func TestImportPasswdFileFromAFileWithTextOutput(t *testing.T) {
	s, got := passwdFileServer(t)
	path := filepath.Join(t.TempDir(), "users")
	if err := os.WriteFile(path, []byte("alice@example.test:{SHA512-CRYPT}"+cliImportHash+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var out, errs bytes.Buffer
	code := runImportPasswdFile(context.Background(), []string{"--server", s.URL, path}, strings.NewReader(""), "private-key", s.Client(), &out, &errs)
	if code != exitOK || len(*got) != 1 || !strings.Contains(out.String(), "Created 1 accounts; 0 already existed") {
		t.Fatalf("exit %d, %d imports, stdout %q, stderr %q", code, len(*got), &out, &errs)
	}
}

func TestImportPasswdFileDryRunSendsNothing(t *testing.T) {
	var out, errs bytes.Buffer
	code := runImportPasswdFile(context.Background(), []string{"--dry-run", "--domain", "example.test", "--json"},
		strings.NewReader(passwdFile), "", &http.Client{Transport: importMustNotRequest{t}}, &out, &errs)
	var outcome map[string][]passwdFileResult
	if err := json.Unmarshal(out.Bytes(), &outcome); err != nil {
		t.Fatal(err)
	}
	if code != exitFailed || len(outcome["would_create"]) != 4 || len(outcome["failed"]) != 2 || len(outcome["created"]) != 0 {
		t.Fatalf("exit %d, outcome %+v", code, outcome)
	}
}

func TestImportPasswdFileUsage(t *testing.T) {
	for _, tc := range []struct {
		name  string
		args  []string
		token string
	}{
		{"no API key", []string{"--server", "http://localhost", "--domain", "example.test"}, ""},
		{"no server", []string{"--domain", "example.test"}, "private-key"},
		{"bad domain", []string{"--server", "http://localhost", "--domain", "not a domain"}, "private-key"},
		{"two files", []string{"--server", "http://localhost", "a", "b"}, "private-key"},
		{"missing file", []string{"--server", "http://localhost", filepath.Join(t.TempDir(), "absent")}, "private-key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out, errs bytes.Buffer
			code := runImportPasswdFile(context.Background(), tc.args, strings.NewReader(passwdFile), tc.token, &http.Client{Transport: importMustNotRequest{t}}, &out, &errs)
			if code != exitUsage {
				t.Fatalf("exit %d, stderr %s", code, &errs)
			}
		})
	}
}

// A user written without a domain needs --domain; with none, that line fails
// and the others are still imported.
func TestImportPasswdFileUserWithoutDomain(t *testing.T) {
	s, got := passwdFileServer(t)
	var out, errs bytes.Buffer
	code := runImportPasswdFile(context.Background(), []string{"--server", s.URL, "--json"},
		strings.NewReader("bob:{SHA512-CRYPT}"+cliImportHash+"\nalice@example.test:{SHA512-CRYPT}"+cliImportHash+"\n"),
		"private-key", s.Client(), &out, &errs)
	var outcome map[string][]passwdFileResult
	_ = json.Unmarshal(out.Bytes(), &outcome)
	if code != exitFailed || len(*got) != 1 || len(outcome["failed"]) != 1 || outcome["failed"][0].Reason != "the user has no domain; give --domain" {
		t.Fatalf("exit %d, %d imports, outcome %+v", code, len(*got), outcome)
	}
}
