package pipeline

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// A duration is written in whole milliseconds under the duration_ms it is named for, not in
// a time.Duration's nanoseconds, and read back the same way.
func TestElapsedIsWrittenInMilliseconds(t *testing.T) {
	result := ExecutionResult{
		FinalAction: ActionContinue,
		Steps: []StepResult{{
			FilterName: "spf_check",
			Duration:   Elapsed(1500 * time.Millisecond),
			Log:        FilterLog{Filter: "spf_check", Duration: Elapsed(1200 * time.Microsecond)},
		}},
		Duration: Elapsed(42 * time.Millisecond),
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"duration_ms":42`, `"duration_ms":1500`, `"log":{"filter":"spf_check","result":"","duration_ms":1}`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("%s lacks %s", data, want)
		}
	}

	var back ExecutionResult
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if time.Duration(back.Duration) != 42*time.Millisecond || time.Duration(back.Steps[0].Duration) != 1500*time.Millisecond {
		t.Errorf("read back: %v, %v", time.Duration(back.Duration), time.Duration(back.Steps[0].Duration))
	}

	// A log with no duration leaves it out.
	if data, _ := json.Marshal(FilterLog{Filter: "x"}); strings.Contains(string(data), "duration_ms") {
		t.Errorf("an unset duration is omitted: %s", data)
	}
}
