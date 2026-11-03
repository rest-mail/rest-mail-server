package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
)

// /api/docs/openapi.yaml serves the root openapi.yaml, byte for byte (#360).
// The served spec used to be a hand-kept copy in this package, and it fell
// about 1,200 lines behind; if a copy ever comes back, this fails as soon as
// the two differ.
func TestDocs_ServesTheRootSpec(t *testing.T) {
	want, err := os.ReadFile("../../openapi.yaml")
	if err != nil {
		t.Fatalf("read the root spec: %v", err)
	}

	router, _ := newTestRouter(t)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/docs/openapi.yaml", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/docs/openapi.yaml = %d, want 200", rec.Code)
	}
	if !bytes.Equal(rec.Body.Bytes(), want) {
		t.Errorf("the served spec is not openapi.yaml: served %d bytes, the file has %d", rec.Body.Len(), len(want))
	}
	if rec.Header().Get("Content-Type") != "application/yaml" || rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf("headers = %v", rec.Header())
	}
}

// /metrics is scraped with GET from inside the metrics CIDRs; a peer outside them is
// answered 404 before the route is reached, and another method is refused. (The test
// router's database fails, so its scrape cannot succeed: TestExposition covers the answer.)
func TestMetrics_ScrapedWithGETFromInside(t *testing.T) {
	router, _ := newTestRouter(t)
	scrape := func(method, peer string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/metrics", nil)
		req.RemoteAddr = peer
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	if rec := scrape(http.MethodGet, "203.0.113.7:5000"); rec.Code != http.StatusNotFound {
		t.Fatalf("an outside peer got %d, want 404", rec.Code)
	}
	if rec := scrape(http.MethodPost, "127.0.0.1:5000"); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /metrics = %d, want 405", rec.Code)
	}
}

// The exposition answers what the Prometheus handler wrote, its negotiated content type and
// encoding included; a failed scrape is the API's 500.
func TestExposition(t *testing.T) {
	answer := exposition(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write([]byte("gzipped exposition"))
	}))
	doc, err := answer(httptest.NewRequest(http.MethodGet, "/metrics", nil), struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	if doc.ContentType != "text/plain; version=0.0.4; charset=utf-8" || doc.Header.Get("Content-Encoding") != "gzip" ||
		doc.Header.Get("Content-Type") != "" || string(doc.Body) != "gzipped exposition" {
		t.Fatalf("answered %+v", doc)
	}

	failing := exposition(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "An error has occurred while serving metrics", http.StatusInternalServerError)
	}))
	if _, err := failing(httptest.NewRequest(http.MethodGet, "/metrics", nil), struct{}{}); err == nil {
		t.Fatal("a failed scrape was answered as a success")
	}
}

// The documentation page is HTML that loads the description generated from the registered
// routes (#670), and loads Swagger UI from one exact release whose files it checks by hash.
func TestDocs_ServesTheViewer(t *testing.T) {
	router, _ := newTestRouter(t)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/docs", nil))
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("GET /api/docs = %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	page := rec.Body.String()
	if !strings.Contains(page, "url: '/api/docs/openapi.json'") || strings.Contains(page, "openapi.yaml") {
		t.Errorf("the page does not load the generated description /api/docs/openapi.json:\n%s", page)
	}

	if !regexp.MustCompile(`^\d+\.\d+\.\d+$`).MatchString(swaggerUIVersion) {
		t.Fatalf("swaggerUIVersion %q is not an exact release", swaggerUIVersion)
	}
	external := regexp.MustCompile(`<(?:script|link)\b[^>]*\b(?:src|href)="https?://[^"]*"[^>]*>`).FindAllString(page, -1)
	if len(external) != 2 {
		t.Fatalf("want the stylesheet and the script as external assets, found %d: %q", len(external), external)
	}
	for _, tag := range external {
		if !strings.Contains(tag, "https://unpkg.com/swagger-ui-dist@"+swaggerUIVersion+"/") {
			t.Errorf("%s does not load swagger-ui-dist %s", tag, swaggerUIVersion)
		}
		if !regexp.MustCompile(`\bintegrity="sha384-[A-Za-z0-9+/]{64}"`).MatchString(tag) ||
			!strings.Contains(tag, `crossorigin="anonymous"`) {
			t.Errorf("%s has no SHA-384 integrity and anonymous crossorigin", tag)
		}
	}
}
