package api

import (
	"bytes"
	"fmt"
	"net/http"
	"sync"

	oapifly "github.com/antimatter-studios/go-oapifly"

	"github.com/restmail/restmail"
	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/version"
)

// registerDocs mounts the API's description and the page that shows it: openapi.yaml as
// written, and the description generated from the registered routes (#670).
func registerDocs(rs endpoint.Routes) {
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/docs/openapi.json",
		OperationID: "openapiDocument", Summary: "This API's description, generated from its routes", Tags: []string{"System"},
		Description: "The OpenAPI 3.1 description go-oapifly generates from the routes this server registers, " +
			"the description itself among them. It replaces openapi.yaml once the two agree (#670).",
		Security: oapifly.Public, MediaType: "application/json",
		Responses: []oapifly.EndpointResponse{{Status: http.StatusOK, Type: oapifly.TypeOf[openAPIDescription]()}}}, generatedDescription(rs.Registry))
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/docs",
		OperationID: "swaggerUI", Summary: "Swagger UI documentation page", Tags: []string{"System"},
		Security: oapifly.Public, MediaType: "text/html"}, swaggerUI)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/docs/openapi.yaml",
		OperationID: "openapiSpec", Summary: "This OpenAPI specification document", Tags: []string{"System"},
		Security: oapifly.Public, MediaType: "application/yaml"}, openAPISpec)
}

// openAPIDescription states OpenAPI 3.1's required document fields. Its other
// fields and the contents of Info and Paths belong to the OpenAPI vocabulary,
// including extensions, rather than REST Mail's closed resource DTOs. The
// handler still serves the generator's bytes without an envelope or conversion.
type openAPIDescription struct {
	_       struct{}       `oapi:"open"`
	OpenAPI string         `json:"openapi"`
	Info    map[string]any `json:"info"`
	Paths   map[string]any `json:"paths"`
}

// swaggerUI serves a Swagger UI page that loads the embedded OpenAPI spec.
func swaggerUI(*http.Request, struct{}) (endpoint.Document, error) {
	return endpoint.Document{ContentType: "text/html; charset=utf-8", Body: []byte(swaggerHTML)}, nil
}

// generatedDescription answers the description of the routes registry holds, generated on the
// first request, when every route is registered, and the same bytes after that. Any origin may
// read it.
func generatedDescription(registry *endpoint.Registry) func(*http.Request, struct{}) (endpoint.Document, error) {
	var once sync.Once
	var body []byte
	var err error
	return func(*http.Request, struct{}) (endpoint.Document, error) {
		once.Do(func() { body, err = describe(registry, version.Version).JSON() })
		if err != nil {
			return endpoint.Document{}, err
		}
		return endpoint.Document{ContentType: "application/json",
			Header: http.Header{"Access-Control-Allow-Origin": {"*"}}, Body: body}, nil
	}
}

// openAPISpec serves the root openapi.yaml, embedded by the root package, so there is no
// copy of it here to keep in step. Any origin may read it.
func openAPISpec(*http.Request, struct{}) (endpoint.Document, error) {
	data, err := restmail.OpenAPI.ReadFile("openapi.yaml")
	if err != nil {
		return endpoint.Document{}, err
	}
	return endpoint.Document{ContentType: "application/yaml",
		Header: http.Header{"Access-Control-Allow-Origin": {"*"}}, Body: data}, nil
}

// registerMetrics mounts the Prometheus exposition at GET /metrics. The router's metrics
// network gate answers 404 to any peer outside the metrics CIDRs before it is reached.
func registerMetrics(rs endpoint.Routes, metrics http.Handler) {
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/metrics",
		OperationID: "prometheusMetrics", Summary: "Prometheus metrics exposition", Tags: []string{"System"},
		Security: oapifly.Public, MediaType: "text/plain"}, exposition(metrics))
}

// exposition answers what the Prometheus handler writes, headers and all, so content
// negotiation (OpenMetrics, gzip) stays the handler's own. A failed scrape is a 500.
func exposition(metrics http.Handler) func(*http.Request, struct{}) (endpoint.Document, error) {
	return func(r *http.Request, _ struct{}) (endpoint.Document, error) {
		scrape := &capturedResponse{header: http.Header{}}
		metrics.ServeHTTP(scrape, r)
		if scrape.status != http.StatusOK {
			return endpoint.Document{}, fmt.Errorf("metrics: the scrape answered %d: %s", scrape.status, scrape.body.Bytes())
		}
		contentType := scrape.header.Get("Content-Type")
		scrape.header.Del("Content-Type")
		return endpoint.Document{ContentType: contentType, Header: scrape.header, Body: scrape.body.Bytes()}, nil
	}
}

// capturedResponse is what a handler wrote, kept to be answered afterwards.
type capturedResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (c *capturedResponse) Header() http.Header { return c.header }

func (c *capturedResponse) WriteHeader(status int) {
	if c.status == 0 {
		c.status = status
	}
}

func (c *capturedResponse) Write(b []byte) (int, error) {
	c.WriteHeader(http.StatusOK)
	return c.body.Write(b)
}

const swaggerHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <title>RestMail API Documentation</title>
  <link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5/swagger-ui.css">
  <style>
    body { margin: 0; padding: 0; }
    #swagger-ui .topbar { display: none; }
  </style>
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-bundle.js"></script>
  <script>
    SwaggerUIBundle({
      url: '/api/docs/openapi.yaml',
      dom_id: '#swagger-ui',
      deepLinking: true,
      presets: [SwaggerUIBundle.presets.apis, SwaggerUIBundle.SwaggerUIStandalonePreset],
      layout: 'BaseLayout'
    });
  </script>
</body>
</html>`
