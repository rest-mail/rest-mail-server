package endpoint

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/rest-mail/rest-mail-server/internal/requestid"
)

// A handler that fails logs the failure with the request's context, so the line
// carries the request's ID (#1580).
func TestHandlerFailureLineCarriesTheRequestID(t *testing.T) {
	var out bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(requestid.NewHandler(slog.NewJSONHandler(&out, nil))))
	t.Cleanup(func() { slog.SetDefault(previous) })

	r := chi.NewRouter()
	Register(Routes{Router: r, Registry: NewRegistry()}, Operation{Method: http.MethodGet, Path: "/widgets",
		OperationID: "getWidget", Summary: "Get a widget"},
		func(_ *http.Request, _ Empty) (widget, error) { return widget{}, errors.New("the store is gone") })

	req := httptest.NewRequest(http.MethodGet, "/widgets", nil)
	req = req.WithContext(requestid.NewContext(req.Context(), "gw-1580-handler"))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d, want 500", rec.Code)
	}

	var line map[string]any
	if err := json.Unmarshal(out.Bytes(), &line); err != nil {
		t.Fatalf("log output %q: %v", out.String(), err)
	}
	if line["msg"] != "api: handler failed" || line[requestid.Attr] != "gw-1580-handler" {
		t.Fatalf("line %v, want the handler failure with request_id gw-1580-handler", line)
	}
}
