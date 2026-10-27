package api

import (
	"bytes"
	"encoding/json"
	"github.com/restmail/restmail/internal/examplepairs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/restmail/restmail"
)

func assertTestSnapshotBody(t *testing.T, wantRaw json.RawMessage, gotRaw []byte, start, end time.Time) {
	t.Helper()
	var want, got map[string]interface{}
	if json.Unmarshal(wantRaw, &want) != nil || json.Unmarshal(gotRaw, &got) != nil {
		t.Fatal("snapshot response is not JSON")
	}
	if data, ok := got["data"].(map[string]interface{}); ok {
		raw, ok := data["snapshot_at"].(string)
		stamp, err := time.Parse(time.RFC3339Nano, raw)
		if !ok || err != nil || stamp.Before(start.Add(-time.Second)) || stamp.After(end.Add(time.Second)) {
			t.Fatal("snapshot time is absent, invalid or outside the request")
		}
		want["data"].(map[string]interface{})["snapshot_at"] = raw
	}
	encoded, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	assertPublicExampleBody(t, encoded, gotRaw)
}

func TestTestSnapshotExamples(t *testing.T) {
	f := newTestSnapshotScenarios(t)
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs["snapshotTestData"]) < 20 {
		t.Fatal("snapshot examples incomplete")
	}
	for name, pair := range pairs["snapshotTestData"] {
		t.Run(name, func(t *testing.T) {
			s, ok := f[name]
			if !ok {
				t.Fatalf("missing snapshot scenario%s", name)
			}
			before := 0
			if s.store != nil {
				before = len(s.store.snapshot())
			}
			var body []byte
			if name == "request_too_large" {
				body = bytes.Repeat([]byte("x"), 513)
			}
			start := time.Now()
			rr := mailboxSessionEndingRequest(t, s, http.MethodPost, testSnapshotPath, body)
			end := time.Now()
			if rr.Code != pair.Response.Status {
				t.Fatalf("snapshot got%d %s want%d", rr.Code, rr.Body, pair.Response.Status)
			}
			assertTestSnapshotBody(t, pair.Response.Body, rr.Body.Bytes(), start, end)
			selected := []string{}
			if s.store != nil {
				for _, c := range s.store.snapshot()[before:] {
					if c.query == "SNAPSHOT_WRITE" {
						t.Fatal("snapshot attempted to mutate resources")
					}
					if c.query == "SNAPSHOT_CONTEXT" && c.args[0].Value != true {
						t.Fatal("snapshot query lost HTTP context")
					}
					if strings.HasPrefix(c.query, "SELECT *") {
						selected = append(selected, c.query)
					}
				}
			}
			want := []string{}
			if rr.Code == 200 {
				for _, table := range testSnapshotTables {
					want = append(want, `SELECT * FROM "`+table+`"`)
				}
			}
			if strings.HasPrefix(name, "failure_") {
				for _, table := range testSnapshotTables {
					want = append(want, `SELECT * FROM "`+table+`"`)
					if "failure_"+table == name {
						break
					}
				}
			}
			if name == "cancelled" {
				want = []string{`SELECT * FROM "domains"`}
			}
			a, _ := json.Marshal(selected)
			b, _ := json.Marshal(want)
			if !bytes.Equal(a, b) {
				t.Fatalf("snapshot selects%s want%s", a, b)
			}
		})
	}
	for _, g := range gapGenerators(t) {
		for _, w := range g.Report() {
			if w.OperationID == "snapshotTestData" && gapWarningApplies(map[string]gapOperation{"snapshotTestData": {}}, w) {
				t.Errorf("snapshot warning:%v", w)
			}
		}
	}
}

func TestTestSnapshotExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	f := newTestSnapshotScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for name, pair := range pairs["snapshotTestData"] {
		t.Run(name, func(t *testing.T) {
			checkQueueMutationPairWithVertrag(t, binary, f[name], http.MethodPost, testSnapshotPath, name, pair, 513)
		})
	}
}
