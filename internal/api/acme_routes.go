package api

import (
	"fmt"
	"net/http"
	"strings"

	oapifly "github.com/antimatter-studios/go-oapifly"

	"github.com/restmail/restmail/internal/api/endpoint"
)

type acmeChallengePath struct {
	Token string `path:"token"`
}

// registerACME describes the HTTP-01 responder only when ACME is configured.
// Its key authorization remains the responder's exact plain-text answer.
func registerACME(rs endpoint.Routes, responder http.Handler) {
	answer := func(r *http.Request, _ acmeChallengePath) (endpoint.Document, error) {
		captured := &capturedResponse{header: http.Header{}}
		responder.ServeHTTP(captured, r)
		switch captured.status {
		case http.StatusOK:
			contentType := captured.header.Get("Content-Type")
			captured.header.Del("Content-Type")
			return endpoint.Document{ContentType: contentType, Header: captured.header, Body: captured.body.Bytes()}, nil
		case http.StatusNotFound:
			refusal := endpoint.RefuseText(http.StatusNotFound, strings.TrimSuffix(captured.body.String(), "\n"))
			refusal.Header = captured.header
			return endpoint.Document{}, refusal
		default:
			return endpoint.Document{}, fmt.Errorf("ACME challenge responder answered %d", captured.status)
		}
	}
	for _, route := range []struct{ method, id string }{
		{http.MethodGet, "acmeChallenge"},
		{http.MethodHead, "headAcmeChallenge"},
	} {
		endpoint.Register(rs, endpoint.Operation{Method: route.method, Path: "/.well-known/acme-challenge/{token}",
			OperationID: route.id, Summary: "ACME HTTP-01 challenge response", Tags: []string{"System"},
			Description: "Present only when ACME is enabled. An unknown challenge token is refused with plain-text 404.",
			Security:    oapifly.Public, MediaType: "text/plain",
			Responses: []oapifly.EndpointResponse{{Status: http.StatusNotFound, MediaType: "text/plain", Type: oapifly.TypeOf[string](),
				Description: "Unknown challenge token"}}}, answer)
	}
}
