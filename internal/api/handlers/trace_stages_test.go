package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/pipeline"
)

func TestMessageTraceTypedStagesKeepEngineJSON(t *testing.T) {
	stages := []pipeline.StepResult{{
		FilterName: "spf_check", FilterType: pipeline.FilterTypeAction,
		Action: pipeline.ActionContinue, Duration: pipeline.Elapsed(3 * time.Millisecond),
		Log: pipeline.FilterLog{Filter: "spf_check", Result: "pass", Duration: pipeline.Elapsed(time.Millisecond)},
	}, {
		FilterName: "sieve", FilterType: pipeline.FilterTypeTransform, Action: pipeline.ActionContinue,
		Skipped: true, SkipReason: "disabled", Error: "not executed",
	}}
	encoded, err := json.Marshal(stages)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []json.RawMessage{nil, json.RawMessage("null"), json.RawMessage("[]"), encoded} {
		t.Run(string(raw), func(t *testing.T) {
			row := models.MessageTrace{ID: 7, Direction: "inbound", DurationMS: 4, Stages: raw}
			wire, err := messageTraceOf(row)
			if err != nil {
				t.Fatal(err)
			}
			before, _ := json.Marshal(row)
			after, _ := json.Marshal(wire)
			var original, typed any
			if json.Unmarshal(before, &original) != nil || json.Unmarshal(after, &typed) != nil || !reflect.DeepEqual(original, typed) {
				t.Fatalf("trace changed: %s -> %s", before, after)
			}
		})
	}
}

func TestMessageTraceTypedStagesDoNotExposeUndeclaredFields(t *testing.T) {
	wire, err := messageTraceOf(models.MessageTrace{Stages: json.RawMessage(`[{"filter_name":"spf_check","filter_type":"action","action":"continue","duration_ms":3,"storage_ref":"private-stage","log":{"filter":"spf_check","result":"pass","storage_ref":"private-log"}}]`)})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"storage_ref", "private-stage", "private-log"} {
		if strings.Contains(string(body), secret) {
			t.Fatal("stored undeclared data escaped the typed response")
		}
	}
	if wire.Stages == nil || len(*wire.Stages) != 1 || (*wire.Stages)[0].Duration != pipeline.Elapsed(3*time.Millisecond) {
		t.Fatal("declared stage data was not retained")
	}
}

func TestMessageTraceTypedStagesRefuseCorruptStorage(t *testing.T) {
	for _, raw := range []string{`{}`, `[1]`, `[{"duration_ms":"private-value"}]`, `[] trailing`} {
		t.Run(raw, func(t *testing.T) {
			_, err := messageTraceOf(models.MessageTrace{Stages: json.RawMessage(raw)})
			if err == nil || strings.Contains(err.Error(), "private-value") {
				t.Fatalf("corrupt stored stages did not produce a sanitized refusal: %v", err)
			}
		})
	}
}

func TestMessageTraceTypedStagesRegisteredReads(t *testing.T) {
	db := openTraceTestDB(t)
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })
	h := seedTraceReadFixtures(t, tx)
	row := models.MessageTrace{}
	if err := tx.Where("message_id = ?", 1001).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if err := tx.Model(&row).Update("stages", json.RawMessage(`[{"filter_name":"spf_check","filter_type":"action","action":"continue","log":{"filter":"spf_check","result":"pass"},"duration_ms":3,"storage_ref":"private-stage"}]`)).Error; err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v1/admin/messages/1001/trace", "/api/v1/admin/pipelines/logs"} {
		rr := httptest.NewRecorder()
		pipelineRoutes(h).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		if rr.Code != http.StatusOK || strings.Contains(rr.Body.String(), "storage_ref") || strings.Contains(rr.Body.String(), "private-stage") {
			t.Fatalf("registered trace response did not use typed stages: status %d", rr.Code)
		}
	}
}
