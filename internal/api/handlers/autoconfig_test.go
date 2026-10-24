package handlers

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/db/models"
)

// autoconfigRoutes mounts h's discovery routes as routes.go does, behind the Autodiscover body
// cap.
func autoconfigRoutes(h *AutoconfigHandler) http.Handler {
	r := chi.NewRouter()
	h.Register(endpoint.Routes{Router: r, Registry: endpoint.NewRegistry()}, AutoconfigRouteChecks{
		AutodiscoverBody: endpoint.Middleware{Wrap: middleware.MaxBodyBytes(AutodiscoverMaxBody)},
	})
	return r
}

// TestMicrosoftAutodiscover_XMLInjection proves the Microsoft Autodiscover
// response escapes the caller-controlled email local-part instead of reflecting
// it into the XML as markup (issue #202: unauthenticated Autodiscover XML
// reflection / element injection). A local-part carrying <, & and " must appear
// escaped, the response must stay well-formed XML, and the raw injected element
// must not appear.
func TestMicrosoftAutodiscover_XMLInjection(t *testing.T) {
	gdb := openAuthzTestDB(t)
	tx := gdb.Begin()
	t.Cleanup(func() { tx.Rollback() })

	domainName := fmt.Sprintf("auto-%d.test", time.Now().UnixNano())
	if err := tx.Create(&models.Domain{Name: domainName, Active: true}).Error; err != nil {
		t.Fatalf("seed domain: %v", err)
	}

	h := NewAutoconfigHandler(tx, "mail.restmail.test")

	// Malicious local-part with XML metacharacters. Sent via the query-param
	// fallback (empty body → the request XML decode fails → the handler reads
	// emailaddress), so the raw value reaches the reflection point undecoded.
	localPart := `ev<il>&"q`
	email := localPart + "@" + domainName
	target := "/autodiscover/autodiscover.xml?emailaddress=" + url.QueryEscape(email)

	req := httptest.NewRequest(http.MethodPost, target, nil)
	rr := httptest.NewRecorder()
	autoconfigRoutes(h).ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()

	// The response must remain well-formed XML: an unescaped "<il>" would open a
	// bogus element (and the bare "&" a malformed entity), which the decoder
	// rejects.
	dec := xml.NewDecoder(strings.NewReader(body))
	for {
		_, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("response is not well-formed XML (injection succeeded): %v\nbody:\n%s", err, body)
		}
	}

	// The injected element must not be reflected verbatim, and the local-part must
	// appear XML-escaped instead.
	if strings.Contains(body, "<il>") {
		t.Errorf("response contains the raw injected element <il>:\n%s", body)
	}
	if !strings.Contains(body, "&lt;il&gt;") {
		t.Errorf("expected the local-part to be XML-escaped (&lt;il&gt;), body:\n%s", body)
	}

	// Sanity: the response still advertises the two protocols with the domain.
	if !strings.Contains(body, "<Type>IMAP</Type>") || !strings.Contains(body, "<Type>SMTP</Type>") {
		t.Errorf("response missing expected protocol blocks:\n%s", body)
	}
}
