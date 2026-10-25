package handlers

import (
	"mime"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/go-chi/chi/v5"

	"github.com/restmail/restmail/internal/api/endpoint"
)

// Describe the actual attachment registration, then exercise the same Served
// writer and filename formatter the handler uses, without a database or storage.
func TestAttachmentDownloadHeadersDescribeTheWire(t *testing.T) {
	reg := endpoint.NewRegistry()
	NewAttachmentHandler(nil, nil).Register(endpoint.Routes{Router: chi.NewRouter(), Registry: reg})
	var download oapifly.Endpoint
	for _, operation := range reg.Endpoints() {
		if operation.OperationID == "getAttachment" {
			download = operation
		}
	}
	var success oapifly.EndpointResponse
	for _, response := range download.Responses {
		if response.Status == http.StatusOK {
			success = response
			continue
		}
		for _, name := range []string{"Content-Disposition", "Content-Length", "Accept-Ranges"} {
			if _, present := response.Headers[name]; present {
				t.Errorf("error %d declares the successful download's %s", response.Status, name)
			}
		}
	}
	if success.Status != http.StatusOK || success.MediaType != "application/octet-stream" {
		t.Fatalf("missing binary success response: %+v", success)
	}
	g := oapifly.New(oapifly.Config{})
	g.Register(download)
	paths := g.Generate()["paths"].(map[string]map[string]oapifly.PathItem)
	headers := paths[download.Path]["get"].Responses["200"].Headers
	for _, name := range []string{"Content-Disposition", "Content-Length", "Accept-Ranges"} {
		header, present := headers[name]
		if !present || !header.Required || header.Description == "" {
			t.Fatalf("missing required, described %s: %+v", name, header)
		}
		want := "string"
		if name == "Content-Length" {
			want = "integer"
		}
		if header.Schema["type"] != want {
			t.Errorf("%s schema = %v, want %s", name, header.Schema, want)
		}
	}
	for _, test := range []struct{ filename, body string }{
		{"notes.txt", "attachment bytes"},
		{"quote\";name.txt", ""},
		{"résumé.pdf", "nonempty"},
		{"bad\r\nX-Injected: yes.txt", "safe"},
	} {
		t.Run(test.filename, func(t *testing.T) {
			r := chi.NewRouter()
			endpoint.Register(endpoint.Routes{Router: r, Registry: endpoint.NewRegistry()}, endpoint.Operation{
				Method: http.MethodGet, Path: "/download", MediaType: success.MediaType, Headers: success.Headers,
			}, func(*http.Request, struct{}) (endpoint.Served, error) {
				return endpoint.Served{ContentType: success.MediaType, Name: test.filename,
					Header:  http.Header{"Content-Disposition": {contentDispositionAttachment(test.filename)}},
					Content: strings.NewReader(test.body)}, nil
			})
			rr := httptest.NewRecorder()
			r.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/download", nil))
			if rr.Code != http.StatusOK || rr.Body.String() != test.body {
				t.Fatalf("download = %d %q", rr.Code, rr.Body.String())
			}
			if rr.Header().Get("Content-Type") != success.MediaType || rr.Header().Get("Accept-Ranges") != "bytes" {
				t.Fatalf("wrong download headers: %v", rr.Header())
			}
			length, err := strconv.ParseInt(rr.Header().Get("Content-Length"), 10, 64)
			if err != nil || length != int64(len(test.body)) {
				t.Fatalf("Content-Length = %q, want %d: %v", rr.Header().Get("Content-Length"), len(test.body), err)
			}
			disposition, params, err := mime.ParseMediaType(rr.Header().Get("Content-Disposition"))
			if err != nil || disposition != "attachment" || params["filename"] != strings.ReplaceAll(strings.ReplaceAll(test.filename, "\r", ""), "\n", "") || rr.Header().Get("X-Injected") != "" {
				t.Fatalf("unsafe or incorrect disposition %q: %v", rr.Header().Get("Content-Disposition"), err)
			}
		})
	}
}
