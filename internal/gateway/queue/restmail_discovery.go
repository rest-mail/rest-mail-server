package queue

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	apitypes "github.com/restmail/restmail/internal/api/types"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// maxRESTMAILDiscoveryBody caps the discovery response read. The document is a
// single small JSON object; anything larger is refused rather than buffered.
const maxRESTMAILDiscoveryBody = 4 << 10

// errRESTMAILNotHosted is a verified "this MX does not deliver REST for the
// domain": a 404 or 410 from the MX host's discovery endpoint over HTTPS whose
// certificate matched the MX host name (#291).
var errRESTMAILNotHosted = errors.New("RESTMAIL discovery: domain not hosted by MX")

// restmailDiscoveryURL returns the discovery URL for the recipient domain
// (IDNA ASCII form) on the MX host mxHost, a name from the domain's MX records.
func restmailDiscoveryURL(mxHost, domain string) string {
	return "https://" + strings.TrimSuffix(mxHost, ".") + "/.well-known/restmail?domain=" + url.QueryEscape(domain)
}

// discoverRESTMAILEndpoint asks discoveryURL, the MX host's discovery endpoint
// (restmailDiscoveryURL), where to deliver REST mail for the domain (#291). The
// EHLO RESTMAIL keyword only says the MX speaks REST; the final endpoint comes
// from this answer, which may name any host (for example the server in a fleet
// that holds the domain).
//
// The answer is trusted only because of how it is fetched: HTTPS to the MX host
// name with the certificate always verified — the worker's tlsInsecure dev flag
// does not apply — through the guarded dialer, no redirects, and a capped body.
// A 404 or 410 returns errRESTMAILNotHosted; any other failure is an ordinary
// error and says nothing about whether the domain speaks REST.
func (w *Worker) discoverRESTMAILEndpoint(discoveryURL string) (string, error) {
	dialer := w.newGuardedDialer(10 * time.Second)
	httpClient := &http.Client{
		Timeout: w.sendDeadline(0),
		Transport: &http.Transport{
			DialContext:         dialer.DialContext,
			TLSClientConfig:     &tls.Config{RootCAs: w.restmailRoots, MinVersion: tls.VersionTLS12},
			TLSHandshakeTimeout: 10 * time.Second,
		},
		// A redirect would move the question to a host whose certificate was
		// never checked against the MX name.
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := httpClient.Get(discoveryURL)
	if err != nil {
		return "", fmt.Errorf("RESTMAIL discovery %s: %w", discoveryURL, err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return "", errRESTMAILNotHosted
	case resp.StatusCode != http.StatusOK:
		return "", fmt.Errorf("RESTMAIL discovery %s got %d", discoveryURL, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxRESTMAILDiscoveryBody+1))
	if err != nil {
		return "", fmt.Errorf("RESTMAIL discovery %s: read: %w", discoveryURL, err)
	}
	if len(body) > maxRESTMAILDiscoveryBody {
		return "", fmt.Errorf("RESTMAIL discovery %s: response exceeds %d bytes", discoveryURL, maxRESTMAILDiscoveryBody)
	}
	var doc apitypes.RestmailDiscovery
	if err := json.Unmarshal(body, &doc); err != nil {
		return "", fmt.Errorf("RESTMAIL discovery %s: decode: %w", discoveryURL, err)
	}
	endpoint, err := validRESTMAILEndpoint(doc.Endpoint)
	if err != nil {
		return "", fmt.Errorf("RESTMAIL discovery %s: %w", discoveryURL, err)
	}
	return endpoint, nil
}

// validRESTMAILEndpoint checks a discovered endpoint is an absolute https URL
// with a host and nothing a sender would have to interpret: no credentials,
// query or fragment. It returns the URL without a trailing slash, ready for
// "/messages" to be appended.
func validRESTMAILEndpoint(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("endpoint %q invalid: %w", raw, err)
	}
	if u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return "", fmt.Errorf("endpoint %q refused: want an https URL with a host and no credentials, query or fragment", raw)
	}
	return strings.TrimSuffix(raw, "/"), nil
}
