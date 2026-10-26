package api

import (
	"encoding/json"
	"mime"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/restmail/restmail"
)

const attachmentDownloadPath = "/api/v1/attachments/{id}"

func TestAttachmentDownloadExamples(t *testing.T) {
	scenarios, fake := attachmentDownloadScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs["getAttachment"]) < 25 {
		t.Fatal("incomplete download examples")
	}
	for name, pair := range pairs["getAttachment"] {
		t.Run(name, func(t *testing.T) {
			s, ok := scenarios[name]
			if !ok {
				t.Fatalf("missing scenario %s", name)
			}
			req := httptest.NewRequest(http.MethodGet, messageCollectionReadURI(attachmentDownloadPath, pair), nil)
			if s.token != "" {
				req.Header.Set("Authorization", "Bearer "+s.token)
			}
			if name == "request_too_large" {
				req.ContentLength = 9
			}
			rr := httptest.NewRecorder()
			before := fake.Requests.Load()
			s.handler.ServeHTTP(rr, req)
			if rr.Code != pair.Response.Status || rr.Header().Get("Content-Type") != pair.Response.MediaType {
				t.Fatalf("got %d %s %s, want %d %s", rr.Code, rr.Header().Get("Content-Type"), rr.Body.String(), pair.Response.Status, pair.Response.MediaType)
			}
			if pair.Response.Status == 200 {
				var body string
				if err := json.Unmarshal(pair.Response.Body, &body); err != nil {
					t.Fatal(err)
				}
				if rr.Body.String() != body {
					t.Fatalf("download bytes differ: %q", rr.Body.String())
				}
				if rr.Header().Get("Content-Length") != strconv.Itoa(len(body)) || rr.Header().Get("Accept-Ranges") != "bytes" {
					t.Fatalf("download lost length/range headers: %v", rr.Header())
				}
				disposition, params, err := mime.ParseMediaType(rr.Header().Get("Content-Disposition"))
				want := "résumé.htmlX-Injected: yes"
				if name == "empty" {
					want = "empty.txt"
				}
				if err != nil || disposition != "attachment" || params["filename"] != want || rr.Header().Get("X-Injected") != "" {
					t.Fatalf("unsafe disposition %q", rr.Header().Get("Content-Disposition"))
				}
				if fake.Requests.Load() <= before {
					t.Fatal("download did not reach real S3 client")
				}
			} else {
				assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
				if name != "source_missing" && name != "object_unavailable" && fake.Requests.Load() != before {
					t.Fatal("refusal reached object storage")
				}
			}
			if name == "rate_limited" {
				n, err := strconv.Atoi(rr.Header().Get("Retry-After"))
				if err != nil || n < 1 {
					t.Fatal("refusal lost Retry-After")
				}
			}
		})
	}
	for _, g := range gapGenerators(t) {
		for _, warning := range g.Report() {
			if warning.OperationID == "getAttachment" {
				t.Errorf("attachment warning: %v", warning)
			}
		}
	}
}

func TestAttachmentDownloadExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY for attachment download exchanges")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	scenarios, _ := attachmentDownloadScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for name, pair := range pairs["getAttachment"] {
		t.Run(name, func(t *testing.T) {
			s := scenarios[name]
			checkMailboxLookupWithVertrag(t, binary, s.handler, s.token, attachmentDownloadPath, name, pair)
		})
	}
}
