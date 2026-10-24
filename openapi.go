// Package restmail is the repository root. It holds only what has to sit here
// to be embedded: go:embed cannot reach a file outside its own package's
// directory, and openapi.yaml lives at the root.
package restmail

import "embed"

// OpenAPI holds openapi.yaml, the only copy of the spec: the API serves it at
// /api/docs/openapi.yaml, and the webmail's types are generated from it. Embed
// it from here rather than copying it next to the code that serves it, so what
// /api/docs describes cannot drift from what is written (#360).
//
//go:embed openapi.yaml
var OpenAPI embed.FS

// Examples holds openapi-example.json, the example pairs of the description the API
// generates from its registrations, beside the document as go-oapifly's convention puts
// them (#625, antimatter-studios/go-oapifly#20).
//
//go:embed openapi-example.json
var Examples embed.FS
