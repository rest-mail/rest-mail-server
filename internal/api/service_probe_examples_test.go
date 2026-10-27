package api

import (
	"bytes"
	"encoding/json"
	"github.com/restmail/restmail/internal/examplepairs"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/restmail/restmail"
)

func assertServiceProbeBody(t *testing.T, wantRaw json.RawMessage, gotRaw []byte) {
	t.Helper()
	var want, got map[string]interface{}
	if err := json.Unmarshal(wantRaw, &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(gotRaw, &got); err != nil {
		t.Fatal(err)
	}
	if expected, ok := want["data"].(map[string]interface{}); ok {
		actual, ok := got["data"].(map[string]interface{})
		if !ok {
			t.Fatal("probe data missing")
		}
		for service, value := range expected {
			e := value.(map[string]interface{})
			a, ok := actual[service].(map[string]interface{})
			if !ok {
				t.Fatal("requested probe result missing")
			}
			if _, measured := e["latency"]; measured {
				latency, ok := a["latency"].(string)
				if !ok {
					t.Fatal("probe latency missing or not a string")
				}
				d, err := time.ParseDuration(latency)
				if err != nil || d < 0 {
					t.Fatal("probe latency is not a nonnegative duration")
				}
				e["latency"] = latency
			}
		}
	}
	wantBytes, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	assertPublicExampleBody(t, wantBytes, gotRaw)
}

func TestServiceProbeExamples(t *testing.T) {
	f := newServiceProbeScenarios(t)
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	for name, pair := range pairs["probeServices"] {
		t.Run(name, func(t *testing.T) {
			s, ok := f[name]
			if !ok {
				t.Fatal("missing service probe scenario")
			}
			body := []byte(pair.Request.Body)
			if name == "malformed_json" {
				body = []byte("{")
			}
			if name == "request_too_large" {
				body = bytes.Repeat([]byte("x"), 513)
			}
			if name == "body_boundary" {
				body = append(body, bytes.Repeat([]byte(" "), 512-len(body))...)
			}
			before := 0
			if s.store != nil {
				before = len(s.store.snapshot())
			}
			rr := mailboxSessionEndingRequest(t, s, http.MethodPost, serviceProbePath, body)
			if rr.Code != pair.Response.Status {
				t.Fatalf("got%d want%d", rr.Code, pair.Response.Status)
			}
			assertServiceProbeBody(t, pair.Response.Body, rr.Body.Bytes())
			pings := 0
			if s.store != nil {
				for _, call := range s.store.snapshot()[before:] {
					if call.query == "PING" {
						pings++
						if len(call.args) != 2 || call.args[0].Value != true || call.args[1].Value != true {
							t.Fatal("database probe lost HTTP context or timeout")
						}
					}
				}
			}
			wantPings := 0
			if rr.Code == 200 {
				wantPings = 1
				if name == "duplicate" {
					wantPings = 2
				}
			}
			if pings != wantPings {
				t.Fatalf("pings%d want%d", pings, wantPings)
			}
		})
	}
	for _, g := range gapGenerators(t) {
		for _, w := range g.Report() {
			if w.OperationID == "probeServices" && gapWarningApplies(map[string]gapOperation{"probeServices": {}}, w) {
				t.Errorf("probe warning:%v", w)
			}
		}
	}
}

func TestServiceProbeExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	f := newServiceProbeScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for name, pair := range pairs["probeServices"] {
		t.Run(name, func(t *testing.T) {
			checkQueueMutationPairWithVertrag(t, binary, f[name], http.MethodPost, serviceProbePath, name, pair, 513)
		})
	}
}

func TestServiceProbeProductionBoundary(t *testing.T) {
	f := newServiceProbeScenarios(t)
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	for name, pair := range pairs["probeServices"] {
		t.Run(name, func(t *testing.T) {
			rr := mailboxSessionEndingRequest(t, f["production"], http.MethodPost, serviceProbePath, pair.Request.Body)
			if rr.Code != 404 {
				t.Fatalf("production probe status%d want404", rr.Code)
			}
		})
	}
}
