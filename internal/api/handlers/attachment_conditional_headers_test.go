package handlers

import (
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail/internal/api/endpoint"
)

// This exercises the real download registration and Served writer together.
// Storage/authorization are isolated: no claim is made about a live attachment.
func TestAttachmentConditionalResponsesDescribeTheWire(t *testing.T) {
	reg := endpoint.NewRegistry()
	NewAttachmentHandler(nil, nil).Register(endpoint.Routes{Router: chi.NewRouter(), Registry: reg})
	var download oapifly.Endpoint
	for _, op := range reg.Endpoints() {
		if op.OperationID == "getAttachment" {
			download = op
		}
	}
	g := oapifly.New(oapifly.Config{})
	g.Register(download)
	paths := g.Generate()["paths"].(map[string]map[string]oapifly.PathItem)
	responses := paths[download.Path]["get"].Responses
	modified := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, header, value, body, media, contentRange string
		status                                         int
		multipart, empty, seekFailure, handlerFailure  bool
	}{
		{name: "whole", status: 200, body: "0123456789", media: "application/octet-stream"},
		{name: "single range", header: "Range", value: "bytes=2-4", status: 206, body: "234", media: "application/octet-stream", contentRange: "bytes 2-4/10"},
		{name: "suffix range", header: "Range", value: "bytes=-3", status: 206, body: "789", media: "application/octet-stream", contentRange: "bytes 7-9/10"},
		{name: "matching If-Range", header: "If-Range", value: `"fixture-etag"`, status: 206, body: "234", media: "application/octet-stream", contentRange: "bytes 2-4/10"},
		{name: "mismatched If-Range fallback", header: "If-Range", value: `"different-etag"`, status: 200, body: "0123456789", media: "application/octet-stream"},
		{name: "multiple ranges", header: "Range", value: "bytes=0-1,8-9", status: 206, media: "multipart/byteranges", multipart: true},
		{name: "unchanged date", header: "If-Modified-Since", value: modified.Format(http.TimeFormat), status: 304},
		{name: "unchanged etag", header: "If-None-Match", value: `"fixture-etag"`, status: 304},
		{name: "failed etag", header: "If-Match", value: `"different-etag"`, status: 412},
		{name: "failed date", header: "If-Unmodified-Since", value: modified.Add(-time.Hour).Format(http.TimeFormat), status: 412},
		{name: "invalid range", header: "Range", value: "bytes=invalid", status: 416, media: "text/plain"},
		{name: "unsatisfiable range", header: "Range", value: "bytes=100-120", status: 416, media: "text/plain", contentRange: "bytes */10"},
		{name: "empty range fallback", header: "Range", value: "bytes=0-1", status: 200, media: "application/octet-stream", empty: true},
		{name: "seek failure", status: 500, media: "text/plain", seekFailure: true},
		{name: "handler failure", status: 500, media: "application/json", handlerFailure: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var content *attachmentConditionalReader
			var bound attachmentPath
			r := chi.NewRouter()
			endpoint.Register(endpoint.Routes{Router: r, Registry: endpoint.NewRegistry()}, endpoint.Operation{
				Method: "GET", Path: "/api/v1/attachments/{id}", MediaType: "application/octet-stream", Responses: download.Responses,
			}, func(_ *http.Request, in attachmentPath) (endpoint.Served, error) {
				bound = in
				if tc.handlerFailure {
					return endpoint.Served{}, errors.New("private attachment lookup failure")
				}
				body := "0123456789"
				if tc.empty {
					body = ""
				}
				content = &attachmentConditionalReader{Reader: strings.NewReader(body), failSeek: tc.seekFailure}
				return endpoint.Served{ContentType: "application/octet-stream", Name: "fixture.txt", Modified: modified,
					Header: http.Header{"Content-Disposition": {contentDispositionAttachment("fixture.txt")}, "Etag": {`"fixture-etag"`}}, Content: content}, nil
			})
			req := httptest.NewRequest("GET", "/api/v1/attachments/1", nil)
			if tc.header == "If-Range" {
				req.Header.Set("Range", "bytes=2-4")
			}
			if tc.header != "" {
				req.Header.Set(tc.header, tc.value)
			}
			rr := httptest.NewRecorder()
			r.ServeHTTP(rr, req)
			if bound.ID != 1 {
				t.Fatal("actual attachment path type was not bound")
			}
			if tc.header != "" {
				values := map[string]string{"Range": bound.Range, "If-Range": bound.IfRange, "If-Match": bound.IfMatch, "If-None-Match": bound.IfNoneMatch, "If-Modified-Since": bound.IfModifiedSince, "If-Unmodified-Since": bound.IfUnmodifiedSince}
				if values[tc.header] != tc.value {
					t.Fatalf("conditional header was not bound: %s", tc.header)
				}
			}
			if rr.Code != tc.status {
				t.Fatalf("status=%d want=%d body=%s", rr.Code, tc.status, rr.Body)
			}
			if content != nil && !content.closed {
				t.Fatal("conditional/error response did not close the stored content")
			}
			described, present := responses[strconv.Itoa(rr.Code)]
			if !present {
				t.Fatalf("wire status %d is not described", rr.Code)
			}
			for name, header := range described.Headers {
				actual := rr.Header().Get(name)
				if header.Required && actual == "" {
					t.Errorf("missing required %s on %d: %v", name, rr.Code, rr.Header())
				}
				if actual != "" && header.Schema["type"] == "integer" {
					if _, err := strconv.ParseInt(actual, 10, 64); err != nil {
						t.Errorf("%s is not an integer: %q", name, actual)
					}
				}
			}
			media, params, _ := mime.ParseMediaType(rr.Header().Get("Content-Type"))
			if tc.media != "" {
				if media != tc.media {
					t.Fatalf("media=%s want=%s", media, tc.media)
				}
				if _, present := described.Content[media]; !present {
					t.Errorf("wire %d media %s is not described: %v", rr.Code, media, described.Content)
				}
			} else if len(described.Content) != 0 || rr.Body.Len() != 0 {
				t.Errorf("bodyless status has body/schema: %q %+v", rr.Body, described.Content)
			}
			if rr.Header().Get("Content-Range") != tc.contentRange {
				t.Errorf("Content-Range=%q want=%q", rr.Header().Get("Content-Range"), tc.contentRange)
			}
			if rr.Code == 200 || rr.Code == 206 {
				if rr.Header().Get("Accept-Ranges") != "bytes" || rr.Header().Get("Content-Length") != strconv.Itoa(rr.Body.Len()) {
					t.Errorf("wrong byte headers: %v body size=%d", rr.Header(), rr.Body.Len())
				}
			}
			if tc.multipart {
				parts := multipart.NewReader(strings.NewReader(rr.Body.String()), params["boundary"])
				for i, want := range []string{"01", "89"} {
					part, err := parts.NextPart()
					if err != nil {
						t.Fatal(err)
					}
					body, err := io.ReadAll(part)
					if err != nil || string(body) != want || part.Header.Get("Content-Type") != "application/octet-stream" || part.Header.Get("Content-Range") != []string{"bytes 0-1/10", "bytes 8-9/10"}[i] {
						t.Fatalf("invalid range part %d: %q %v", i, body, part.Header)
					}
				}
				if _, err := parts.NextPart(); err != io.EOF {
					t.Fatalf("extra multipart range: %v", err)
				}
			} else if rr.Code < 400 && rr.Body.String() != tc.body {
				t.Errorf("body=%q want=%q", rr.Body, tc.body)
			}
			if strings.Contains(rr.Body.String(), "private") {
				t.Fatal("private storage/handler failure escaped")
			}
		})
	}
}

type attachmentConditionalReader struct {
	*strings.Reader
	failSeek, closed bool
}

func (c *attachmentConditionalReader) Seek(offset int64, whence int) (int64, error) {
	if c.failSeek {
		return 0, errors.New("private seek failure")
	}
	return c.Reader.Seek(offset, whence)
}

func (c *attachmentConditionalReader) Close() error { c.closed = true; return nil }
