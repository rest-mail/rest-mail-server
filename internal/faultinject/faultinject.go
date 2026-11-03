//go:build faultinject

// Package faultinject breaks the API on purpose, so a contract run can be
// required to notice (#654). It is compiled only with the faultinject build
// tag, and each fault only with a tag of its own as well, so a normal build
// holds none of this code.
//
// tests/contract/faults.yml is the catalogue: each fault, the build tags that
// apply it and the contract phase that must turn red. A fault is one file here,
// fault_<name>.go, built with the tag fault_<name>, whose init registers it.
package faultinject

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"

	"github.com/rest-mail/rest-mail-server/internal/config"
)

// A Fault breaks the contract, with one route as its witness.
type Fault struct {
	Name   string
	Method string
	Path   string
	// Break rewrites the recorded answer before it is sent.
	Break func(*httptest.ResponseRecorder)
	// BreakSeeing is Break for a fault that needs what the client sent: it is
	// given the request body as well. The route still receives that body.
	BreakSeeing func(sent []byte, rec *httptest.ResponseRecorder)
	// BreakRequest changes the request before the router sees it, for a fault
	// that gets a request past a guard the route sits behind.
	BreakRequest func(*http.Request)
	Configure    func(*config.Config)
	// Example is an answer the route gives without the fault, for the
	// package's own test that the fault changes it. ExampleRequest is the body
	// sent for it, which a BreakSeeing needs.
	Example        string
	ExampleStatus  int
	ExampleRequest string
}

var registry = map[string]Fault{}

func Configure(cfg *config.Config) {
	for _, name := range Active() {
		if change := registry[name].Configure; change != nil {
			change(cfg)
		}
	}
}

// register is called from the init of each fault's file.
func register(f Fault) {
	if _, taken := registry[f.Name]; taken {
		panic("faultinject: fault registered twice: " + f.Name)
	}
	registry[f.Name] = f
}

// Active names the faults compiled into this build.
func Active() []string {
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Wrap breaks the answers of every fault compiled into this build. With none,
// it returns next.
func Wrap(next http.Handler) http.Handler {
	faults := make([]Fault, 0, len(registry))
	for _, name := range Active() {
		f := registry[name]
		// The contract fault run reads this line to know the switch took.
		slog.Warn("fault injection active", "fault", f.Name, "route", f.Method+" "+f.Path)
		faults = append(faults, f)
	}
	return wrap(next, faults)
}

func wrap(next http.Handler, faults []Fault) http.Handler {
	if len(faults) == 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var matched []Fault
		for _, f := range faults {
			if r.Method == f.Method && r.URL.Path == f.Path {
				matched = append(matched, f)
			}
		}
		if len(matched) == 0 {
			next.ServeHTTP(w, r)
			return
		}
		var sent []byte
		if slices.ContainsFunc(matched, func(f Fault) bool { return f.BreakSeeing != nil }) {
			sent, _ = io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(sent))
		}
		for _, f := range matched {
			if f.BreakRequest != nil {
				f.BreakRequest(r)
			}
		}
		rec := httptest.NewRecorder()
		next.ServeHTTP(rec, r)
		for _, f := range matched {
			if f.Break != nil {
				f.Break(rec)
			}
			if f.BreakSeeing != nil {
				f.BreakSeeing(sent, rec)
			}
		}
		for key, values := range rec.Header() {
			w.Header()[key] = values
		}
		// The body may have changed length.
		w.Header().Del("Content-Length")
		w.WriteHeader(rec.Code)
		_, _ = w.Write(rec.Body.Bytes())
	})
}

// renameField moves a top-level JSON field to another name.
func renameField(from, to string) func(*httptest.ResponseRecorder) {
	return editJSON(func(body map[string]any) {
		if value, ok := body[from]; ok {
			body[to] = value
			delete(body, from)
		}
	})
}

// replaceField gives a top-level JSON field a value of another type.
func replaceField(name string, value any) func(*httptest.ResponseRecorder) {
	return editJSON(func(body map[string]any) {
		if _, ok := body[name]; ok {
			body[name] = value
		}
	})
}

// addDataField adds a field to the object under a top-level data field. An
// answer with no such object is left as it is.
func addDataField(name string, value any) func(*httptest.ResponseRecorder) {
	return editJSON(func(body map[string]any) {
		if data, ok := body["data"].(map[string]any); ok {
			data[name] = value
		}
	})
}

// setMediaType answers with another Content-Type and the same body.
func setMediaType(mediaType string) func(*httptest.ResponseRecorder) {
	return func(rec *httptest.ResponseRecorder) {
		rec.Header().Set("Content-Type", mediaType)
	}
}

// editJSON edits a JSON object body; any other body is left as it is.
func editJSON(edit func(map[string]any)) func(*httptest.ResponseRecorder) {
	return func(rec *httptest.ResponseRecorder) {
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			return
		}
		edit(body)
		out, err := json.Marshal(body)
		if err != nil {
			return
		}
		rec.Body.Reset()
		rec.Body.Write(out)
	}
}
