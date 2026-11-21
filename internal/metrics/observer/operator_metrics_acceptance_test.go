package observer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/metrics"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
	"github.com/rest-mail/rest-mail-server/internal/testdb"
	"github.com/rest-mail/rest-mail-server/internal/trace"
)

type failingOperatorFilter struct{ fakeFilter }

func (failingOperatorFilter) Execute(context.Context, *pipeline.EmailJSON) (*pipeline.FilterResult, error) {
	return nil, errors.New("controlled filter failure")
}

func TestAC01PipelineFilterMetricMatchesPersistedStages(t *testing.T) {
	t.Setenv("ENVIRONMENT", "test")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	db := testdb.Clone(t, cfg, "filter_metric", testdb.Models(&models.MessageTrace{}))
	recorder := trace.NewRecorder(db, trace.Config{SampleRate: 1})
	recorder.Start()
	t.Cleanup(recorder.Shutdown)
	registry := prometheus.NewPedanticRegistry()
	registry.MustRegister(metrics.PipelineStageDecisions)
	type series struct{ filter, action string }
	read := func() map[series]int64 {
		t.Helper()
		families, err := registry.Gather()
		if err != nil {
			t.Fatal(err)
		}
		values := make(map[series]int64)
		for _, family := range families {
			for _, sample := range family.Metric {
				labels := make(map[string]string)
				for _, label := range sample.Label {
					labels[label.GetName()] = label.GetValue()
				}
				if len(labels) != 2 || labels["filter"] == "" || labels["action"] == "" {
					t.Fatalf("unexpected pipeline counter labels: %v", labels)
				}
				values[series{labels["filter"], labels["action"]}] = int64(sample.GetCounter().GetValue())
			}
		}
		return values
	}
	before := read()
	filters := pipeline.NewRegistry()
	engine := pipeline.NewEngine(filters, nil, New())
	for index := range 12 {
		name := fmt.Sprintf("customer-filter-%d", index)
		fixture := fakeFilter{name: name, action: pipeline.ActionContinue}
		if index%3 == 0 {
			filters.Register(name, func([]byte) (pipeline.Filter, error) { return failingOperatorFilter{fixture}, nil })
		} else {
			register(filters, fixture)
		}
		envelope := pipeline.Envelope{RcptTo: []string{fmt.Sprintf("recipient%d@tenant%d.test", index, index)}}
		result, err := engine.Execute(t.Context(), &pipeline.PipelineConfig{Direction: "inbound", Filters: []pipeline.FilterConfig{{Name: name, Enabled: true}}}, &pipeline.EmailJSON{Envelope: envelope})
		if err != nil {
			t.Fatal(err)
		}
		stages, err := json.Marshal(result.Steps)
		if err != nil {
			t.Fatal(err)
		}
		recorder.Record(models.MessageTrace{Stages: stages, RcptTo: envelope.RcptTo[0], RFCMessageID: fmt.Sprintf("<%d@fixture.test>", index)})
	}
	recorder.Shutdown()
	var persisted struct{ Total, Failed, Filters, Recipients, Messages int64 }
	if err := db.Raw(`SELECT count(*) AS total, count(*) FILTER (WHERE stage ? 'error') AS failed,
		count(DISTINCT stage->>'filter_name') AS filters, count(DISTINCT rcpt_to) AS recipients,
		count(DISTINCT rfc_message_id) AS messages FROM message_traces CROSS JOIN LATERAL jsonb_array_elements(stages) AS stage`).Scan(&persisted).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.Total != 12 || persisted.Failed != 4 || persisted.Filters != 12 || persisted.Recipients != 12 || persisted.Messages != 12 {
		t.Fatalf("persisted stage oracle: %+v", persisted)
	}
	delta := make(map[series]int64)
	for key, value := range read() {
		if change := value - before[key]; change != 0 {
			delta[key] = change
		}
	}
	want := map[series]int64{{"custom", "error"}: persisted.Failed, {"custom", "continue"}: persisted.Total - persisted.Failed}
	if !maps.Equal(delta, want) {
		t.Fatalf("pipeline stage counter delta = %v, want persisted counts %v", delta, want)
	}
}
