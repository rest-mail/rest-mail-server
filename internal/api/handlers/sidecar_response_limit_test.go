package handlers

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/pipeline/filters"
)

func TestAC02AdminRejectsOversizedSidecarResponses(t *testing.T) {
	const limit = 33 << 20
	db := openCustomFilterCreateScratchDB(t)
	domain := models.Domain{Name: "sidecar-limit.test"}
	if err := db.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	filter := models.CustomFilter{DomainID: domain.ID, Name: "bounded sidecar", FilterType: "transform", Direction: "both", Config: []byte(`{"script":"function filter(e){return e}"}`)}
	if err := db.Create(&filter).Error; err != nil {
		t.Fatal(err)
	}
	for _, route := range []struct{ api, peer, reply string }{
		{"/api/v1/admin/custom-filters/validate", "/validate", `{"valid":true}`},
		{fmt.Sprintf("/api/v1/admin/custom-filters/%d/test", filter.ID), "/execute", `{"result":{"type":"action","action":"continue"},"logs":[]}`},
	} {
		t.Run(route.peer, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != route.peer || r.Header.Get("Authorization") != "Bearer "+testJSSidecarToken {
					t.Errorf("unexpected sidecar request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				w.(http.Flusher).Flush()
				if _, err := io.WriteString(w, route.reply); err != nil {
					return
				}
				padding := strings.Repeat(" ", 32<<10)
				for remaining := limit + 1 - len(route.reply); remaining > 0; remaining -= len(padding) {
					if _, err := io.WriteString(w, padding[:min(len(padding), remaining)]); err != nil {
						return
					}
				}
			}))
			defer server.Close()
			router := pipelineRoutes(NewPipelineHandler(db, nil, filters.JSSidecar{URL: server.URL, Token: testJSSidecarToken}))
			request := httptest.NewRequest(http.MethodPost, route.api, strings.NewReader(`{"script":"function filter(e){return e}"}`))
			if route.peer == "/execute" {
				request = httptest.NewRequest(http.MethodPost, route.api, nil)
			}
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), "Invalid sidecar response") || response.Body.Len() > 1024 {
				t.Fatalf("HTTP %d with %d bytes; want a small 500 refusal: %.180s", response.Code, response.Body.Len(), response.Body)
			}
		})
	}
}
