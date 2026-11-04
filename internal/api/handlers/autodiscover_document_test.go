package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMicrosoftAutodiscover_TrailingXMLRefusedBeforeLookup(t *testing.T) {
	body := `<Autodiscover><Request><EMailAddress>user@example.com</EMailAddress></Request></Autodiscover>`
	router := autoconfigRoutes(NewAutoconfigHandler(nil, "restmail.test"))
	for name, suffix := range map[string]string{
		"text":                "garbage",
		"element":             "<Autodiscover/>",
		"malformed":           "<",
		"declaration":         `<?xml version="1.0"?>`,
		"directive":           "<!DOCTYPE other>",
		"unicode_space":       "\u00a0",
		"character_reference": "&#x20;",
		"cdata":               "<![CDATA[ ]]>",
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, autodiscoverPath, strings.NewReader(body+suffix))
			req.Header.Set("Content-Type", "application/xml")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, req)
			if response.Code != http.StatusBadRequest || response.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("got %d %s: %s", response.Code, response.Header().Get("Content-Type"), response.Body)
			}
			if response.Body.String() != "{\"error\":{\"code\":\"bad_request\",\"message\":\"invalid request\"}}\n" {
				t.Fatalf("unexpected refusal: %s", response.Body)
			}
		})
	}
}
