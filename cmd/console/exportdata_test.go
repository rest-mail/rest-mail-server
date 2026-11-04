package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/quotedprintable"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf8"
)

func runDataExport(t *testing.T, command, server, path string, extra ...string) (int, string, string) {
	t.Helper()
	t.Setenv("RESTMAIL_API_KEY", "private-test-key")
	args := append([]string{"restmail", command, "--server", server, "--account-id", "12", "--path", path, "--json"}, extra...)
	var out, errs bytes.Buffer
	code, handled := runSubcommand(args, &out, &errs)
	if !handled {
		t.Fatal("command not handled")
	}
	if strings.Contains(out.String()+errs.String(), "private-test-key") {
		t.Fatal("credential printed")
	}
	return code, out.String(), errs.String()
}

func TestExportContactsStreamsPagesAsVCard(t *testing.T) {
	path := filepath.Join(t.TempDir(), "contacts.vcf")
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/accounts/12/contacts" || r.Header.Get("Authorization") != "Bearer private-test-key" || r.URL.Query().Get("limit") != "100" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
		}
		switch r.URL.Query().Get("offset") {
		case "0":
			_, _ = fmt.Fprint(w, `{"data":[{"id":1,"email":"alice@example.test","name":"Élodie, A;B\\C\r\nD","trust_level":"blocked","source":"manual","created_at":"2026-11-01T12:00:00Z","updated_at":"2026-11-02T12:00:00Z"}],"pagination":{"has_more":true}}`)
		case "1":
			assertExportStaged(t, path, "*.restmail-export-*", "BEGIN:VCARD")
			_, _ = fmt.Fprint(w, `{"data":[{"id":2,"email":"bob@example.test","name":""}],"pagination":{"has_more":false}}`)
		default:
			t.Errorf("unexpected offset %s", r.URL)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer server.Close()
	code, out, errs := runDataExport(t, "export-contacts", server.URL, path)
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, errs)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{"VERSION:4.0\r\n", `FN:Élodie\, A\;B\\C\nD`, "FN:bob@example.test", "EMAIL:alice@example.test", "X-RESTMAIL-TRUST-LEVEL:blocked", "X-RESTMAIL-SOURCE:manual", "X-RESTMAIL-CREATED-AT:2026-11-01T12:00:00Z", "REV:20261102T120000Z"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in %q", want, text)
		}
	}
	if requests != 2 || strings.Count(text, "BEGIN:VCARD\r\n") != 2 || !strings.Contains(out, `"contacts":2`) {
		t.Fatalf("requests %d, summary %s, output %q", requests, out, text)
	}
	assertPrivateExport(t, path)
}

func assertExportStaged(t *testing.T, path, pattern, contains string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(path), pattern))
	if err != nil || len(matches) != 1 {
		t.Errorf("no single staged export before next page: %v %v", matches, err)
		return
	}
	info, err := os.Stat(matches[0])
	if err != nil {
		t.Error(err)
		return
	}
	file := matches[0]
	if info.IsDir() {
		files, globErr := filepath.Glob(filepath.Join(file, "*.ics"))
		if globErr != nil || len(files) == 0 {
			t.Errorf("no calendar written before next page: %v %v", files, globErr)
			return
		}
		file = files[0]
	}
	raw, err := os.ReadFile(file)
	if err != nil || !strings.Contains(string(raw), contains) {
		t.Errorf("not streamed to disk yet: %q %v", raw, err)
	}
}

func assertPrivateExport(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Errorf("export permissions: %v", info.Mode())
	}
}

const exportedICS = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Other app//EN\r\nMETHOD:REQUEST\r\nBEGIN:VTIMEZONE\r\nTZID:Custom/Zone\r\nEND:VTIMEZONE\r\nBEGIN:VEVENT\r\nUID:event@example.test\r\nDTSTAMP:20261104T120000Z\r\nDTSTART;VALUE=DATE:20261105\r\nSUMMARY:Meeting\\, with friends\r\nRRULE:FREQ=WEEKLY;COUNT=5\r\nX-OTHER-APP:keep this\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"

func TestExportCalendarStreamsOriginalInvitations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "calendar")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer private-test-key" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
		}
		switch r.URL.Path {
		case "/api/v1/accounts/12/folders":
			_, _ = fmt.Fprint(w, `{"data":[{"name":"Archive/2020"}]}`)
		case "/api/v1/accounts/12/folders/Archive/2020/messages":
			if r.URL.Query().Get("include_snoozed") != "true" || r.URL.Query().Get("include_imap_deleted") != "true" || r.URL.Query().Get("limit") != "100" {
				t.Errorf("incomplete message listing: %s", r.URL)
			}
			if r.URL.Query().Get("cursor") == "next" {
				assertExportStaged(t, path, "*.restmail-export-*", "RRULE:FREQ=WEEKLY")
				_, _ = fmt.Fprint(w, `{"data":[{"id":8,"calendar_events":[{}]}],"pagination":{"has_more":false}}`)
			} else {
				_, _ = fmt.Fprint(w, `{"data":[{"id":6},{"id":7,"calendar_events":[{}]}],"pagination":{"has_more":true,"cursor":"next"}}`)
			}
		case "/api/v1/messages/7/raw":
			_, _ = fmt.Fprintf(w, "MIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=outer\r\n\r\n--outer\r\nContent-Type: text/plain\r\n\r\nIgnore me\r\n--outer\r\nContent-Type: multipart/alternative; boundary=inner\r\n\r\n--inner\r\nContent-Type: text/calendar; charset=utf-8\r\nContent-Disposition: attachment; filename=\"../../unsafe.ics\"\r\nContent-Transfer-Encoding: base64\r\n\r\n%s\r\n--inner--\r\n--outer--\r\n", base64.StdEncoding.EncodeToString([]byte(exportedICS)))
		case "/api/v1/messages/8/raw":
			_, _ = fmt.Fprint(w, "Content-Type: text/calendar\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n")
			encoded := quotedprintable.NewWriter(w)
			_, _ = encoded.Write([]byte(strings.ReplaceAll(exportedICS, "METHOD:REQUEST", "METHOD:CANCEL")))
			_ = encoded.Close()
		default:
			t.Errorf("unexpected request %s", r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	code, out, errs := runDataExport(t, "export-calendar", server.URL, path)
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, errs)
	}
	entries, err := os.ReadDir(path)
	if err != nil || len(entries) != 2 || !strings.Contains(out, `"calendars":2`) {
		t.Fatalf("files %v (%v), summary %s", entries, err, out)
	}
	for _, entry := range entries {
		file := filepath.Join(path, entry.Name())
		data, readErr := os.ReadFile(file)
		want := exportedICS
		if strings.HasPrefix(entry.Name(), "8-") {
			want = strings.ReplaceAll(want, "METHOD:REQUEST", "METHOD:CANCEL")
		}
		if readErr != nil || string(data) != want {
			t.Errorf("calendar changed: %s: %q (%v)", entry.Name(), data, readErr)
		}
		assertPrivateExport(t, file)
	}
	assertPrivateExport(t, path)
}

func TestDataExportsDoNotPublishPartialOutput(t *testing.T) {
	for _, command := range []string{"export-contacts", "export-calendar"} {
		for _, failure := range []string{"forbidden", "redirect", "bad_json", "missing_data", "empty_page", "missing_cursor", "missing_source"} {
			if command == "export-contacts" && (failure == "missing_cursor" || failure == "missing_source") {
				continue
			}
			t.Run(command+"/"+failure, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "export")
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/do-not-follow" {
						t.Error("followed an export redirect")
					}
					if r.URL.Path == "/api/v1/accounts/12/folders" {
						_, _ = fmt.Fprint(w, `{"data":[{"name":"INBOX"}]}`)
						return
					}
					switch failure {
					case "forbidden":
						w.WriteHeader(http.StatusForbidden)
					case "redirect":
						w.Header().Set("Location", "/do-not-follow")
						w.WriteHeader(http.StatusTemporaryRedirect)
					case "bad_json":
						_, _ = fmt.Fprint(w, `{"data":[`)
					case "missing_data":
						_, _ = fmt.Fprint(w, `{}`)
					case "empty_page":
						_, _ = fmt.Fprint(w, `{"data":[],"pagination":{"has_more":true,"cursor":"same"}}`)
					case "missing_cursor":
						_, _ = fmt.Fprint(w, `{"data":[{"id":1}],"pagination":{"has_more":true}}`)
					case "missing_source":
						if strings.HasSuffix(r.URL.Path, "/raw") {
							w.WriteHeader(http.StatusNotFound)
						} else {
							_, _ = fmt.Fprint(w, `{"data":[{"id":1,"calendar_events":[{}]}],"pagination":{"has_more":false}}`)
						}
					}
				}))
				defer server.Close()
				code, _, errs := runDataExport(t, command, server.URL, path)
				if code != exitFailed {
					t.Fatalf("exit %d: %s", code, errs)
				}
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Errorf("partial export published: %v", err)
				}
				entries, err := os.ReadDir(filepath.Dir(path))
				if err != nil || len(entries) != 0 {
					t.Errorf("temporary data left: %v (%v)", entries, err)
				}
			})
		}
	}
}

func TestDataExportUsageAndExistingDestinations(t *testing.T) {
	for _, command := range []string{"export-contacts", "export-calendar"} {
		t.Run(command, func(t *testing.T) {
			var out, errs bytes.Buffer
			code, _ := runSubcommand([]string{"restmail", command, "--help"}, &out, &errs)
			if code != exitOK || !strings.Contains(out.String(), "--account-id") {
				t.Fatalf("help: %d %s %s", code, &out, &errs)
			}
			path := filepath.Join(t.TempDir(), "export")
			if err := os.WriteFile(path, []byte("keep me"), 0o600); err != nil {
				t.Fatal(err)
			}
			code, _, message := runDataExport(t, command, "http://localhost:1", path)
			if code != exitUsage {
				t.Fatalf("existing destination: %d %s", code, message)
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != "keep me" {
				t.Fatal("existing destination changed")
			}
			out.Reset()
			errs.Reset()
			code, _ = runSubcommand([]string{"restmail", command, "--json", "--account-id", "0"}, &out, &errs)
			var problem map[string]any
			if code != exitUsage || json.Unmarshal(errs.Bytes(), &problem) != nil {
				t.Fatalf("usage: %d %s", code, &errs)
			}
		})
	}
}

func TestVCardFoldingPreservesUTF8(t *testing.T) {
	for _, value := range []string{strings.Repeat("a", 74) + "猫", strings.Repeat("Élodie猫", 50), strings.Repeat("x", 200)} {
		var output bytes.Buffer
		if err := writeVCardLine(&output, "FN:"+value); err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(strings.TrimSuffix(output.String(), "\r\n"), "\r\n") {
			if len(line) > 75 || !utf8.ValidString(line) {
				t.Fatalf("bad folded line (%d bytes): %q", len(line), line)
			}
		}
		if got := strings.ReplaceAll(output.String(), "\r\n ", ""); got != "FN:"+value+"\r\n" {
			t.Fatalf("unfolding changed text: %q", got)
		}
	}
}

func TestExportContactsOverwriteIsAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "contacts.vcf")
	if err := os.WriteFile(path, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	var fail atomic.Bool
	fail.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("offset") == "0" {
			_, _ = fmt.Fprint(w, `{"data":[{"id":1,"email":"alice@example.test"}],"pagination":{"has_more":true}}`)
		} else if fail.Load() {
			w.WriteHeader(http.StatusForbidden)
		} else {
			_, _ = fmt.Fprint(w, `{"data":[],"pagination":{"has_more":false}}`)
		}
	}))
	defer server.Close()
	code, _, errs := runDataExport(t, "export-contacts", server.URL, path, "--overwrite")
	if code != exitFailed {
		t.Fatalf("exit %d: %s", code, errs)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "keep me" {
		t.Fatal("failed export replaced destination")
	}
	fail.Store(false)
	code, _, errs = runDataExport(t, "export-contacts", server.URL, path, "--overwrite")
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, errs)
	}
	data, err = os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "EMAIL:alice@example.test") {
		t.Fatalf("overwrite failed: %q (%v)", data, err)
	}
}

func TestExportCalendarRefusesUnexportableSource(t *testing.T) {
	for _, problem := range []string{"size", "missing-part", "unknown-encoding", "bad-base64", "repeated-cursor", "late-failure", "too-deep"} {
		t.Run(problem, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "calendar")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/accounts/12/folders":
					_, _ = fmt.Fprint(w, `{"data":[{"name":"INBOX"}]}`)
				case "/api/v1/accounts/12/folders/INBOX/messages":
					if problem == "repeated-cursor" {
						_, _ = fmt.Fprint(w, `{"data":[{"id":1}],"pagination":{"has_more":true,"cursor":"same"}}`)
					} else if problem == "late-failure" {
						_, _ = fmt.Fprint(w, `{"data":[{"id":1,"calendar_events":[{}]},{"id":2,"calendar_events":[{}]}],"pagination":{"has_more":false}}`)
					} else {
						_, _ = fmt.Fprint(w, `{"data":[{"id":1,"calendar_events":[{}]}],"pagination":{"has_more":false}}`)
					}
				case "/api/v1/messages/1/raw":
					switch problem {
					case "missing-part":
						_, _ = fmt.Fprint(w, "Content-Type: text/plain\r\n\r\nnot a calendar")
					case "unknown-encoding":
						_, _ = fmt.Fprint(w, "Content-Type: text/calendar\r\nContent-Transfer-Encoding: not-real\r\n\r\nbad")
					case "bad-base64":
						_, _ = fmt.Fprint(w, "Content-Type: text/calendar\r\nContent-Transfer-Encoding: base64\r\n\r\n%%%")
					case "too-deep":
						for index := range 102 {
							_, _ = fmt.Fprintf(w, "Content-Type: multipart/mixed; boundary=level%d\r\n\r\n--level%d\r\n", index, index)
						}
						_, _ = fmt.Fprint(w, "Content-Type: text/calendar\r\n\r\n"+exportedICS)
						for index := 101; index >= 0; index-- {
							_, _ = fmt.Fprintf(w, "\r\n--level%d--\r\n", index)
						}
					default:
						_, _ = fmt.Fprint(w, "Content-Type: text/calendar\r\n\r\n"+exportedICS)
					}
				case "/api/v1/messages/2/raw":
					assertExportStaged(t, path, "*.restmail-export-*", "BEGIN:VCALENDAR")
					w.WriteHeader(http.StatusForbidden)
				default:
					t.Errorf("unexpected request %s", r.URL)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			var args []string
			if problem == "size" {
				args = []string{"--max-message-bytes", "64"}
			}
			code, _, errs := runDataExport(t, "export-calendar", server.URL, path, args...)
			if code != exitFailed {
				t.Fatalf("exit %d: %s", code, errs)
			}
			entries, err := os.ReadDir(filepath.Dir(path))
			if err != nil || len(entries) != 0 {
				t.Fatalf("partial export left: %v (%v)", entries, err)
			}
		})
	}
}

func TestDataExportsCancelAndRequireAKey(t *testing.T) {
	for _, command := range []string{"export-contacts", "export-calendar"} {
		path := filepath.Join(t.TempDir(), "export")
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		var out, errs bytes.Buffer
		args := []string{"--server", "http://localhost:1", "--account-id", "12", "--path", path, "--json"}
		if code := runExportData(ctx, command, args, "key", http.DefaultClient, &out, &errs); code != exitFailed {
			t.Fatalf("cancelled %s: exit %d: %s", command, code, &errs)
		}
		entries, err := os.ReadDir(filepath.Dir(path))
		if err != nil || len(entries) != 0 {
			t.Fatalf("cancelled export left data: %v (%v)", entries, err)
		}
		if code := runExportData(context.Background(), command, args, "", http.DefaultClient, &out, &errs); code != exitUsage {
			t.Fatalf("missing key %s: exit %d: %s", command, code, &errs)
		}
	}
}

func TestExportMaildirRejectsAListWithoutData(t *testing.T) {
	for _, body := range []string{`{}`, `{"pagination":{"has_more":false}}`} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, body)
			}))
			defer server.Close()
			path := filepath.Join(t.TempDir(), "maildir")
			code, out, errs := runDataExport(t, "export-maildir", server.URL, path)
			if code != exitFailed {
				t.Fatalf("invalid list accepted: exit %d, stdout %s, stderr %s", code, out, errs)
			}
		})
	}
}
