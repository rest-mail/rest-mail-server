package pipeline

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"testing/synctest"
	"time"
)

type singleFilterBoundaryFixture struct {
	mode     string
	failure  error
	release  <-chan struct{}
	finished chan<- struct{}
}

func (singleFilterBoundaryFixture) Name() string { return "fixture" }

func (singleFilterBoundaryFixture) Type() FilterType { return FilterTypeAction }

func (f singleFilterBoundaryFixture) Execute(context.Context, *EmailJSON) (*FilterResult, error) {
	defer close(f.finished)
	switch f.mode {
	case "panic":
		panic("fixture execution panic")
	case "error":
		return nil, f.failure
	case "blocked":
		<-f.release
	}
	return &FilterResult{Type: FilterTypeAction, Action: ActionContinue}, nil
}

// A manual single-filter preview must use the existing execution boundary,
// including its backstop against filters which ignore their context entirely.
func TestSingleFilterExecutionBoundary(t *testing.T) {
	for _, mode := range []string{"success", "error", "panic", "blocked"} {
		run := func(t *testing.T) {
			release := make(chan struct{})
			finished := make(chan struct{})
			failure := errors.New("execution sentinel")
			defer func() {
				close(release)
				select {
				case <-finished:
				case <-time.After(30 * time.Second):
					t.Error("filter goroutine did not finish after release")
				}
			}()
			registry := NewRegistry()
			registry.Register("fixture", func([]byte) (Filter, error) {
				return singleFilterBoundaryFixture{mode: mode, failure: failure, release: release, finished: finished}, nil
			})
			engine := NewEngine(registry, slog.New(slog.NewTextHandler(io.Discard, nil)))
			const deadline = 10 * time.Millisecond
			engine.SetFilterTimeout(deadline)
			started := time.Now()
			result, err := engine.TestFilter(t.Context(), "fixture", []byte(`{}`), &EmailJSON{})
			if mode == "success" {
				if err != nil || result == nil || result.Action != ActionContinue {
					t.Fatalf("successful filter result changed: result=%+v err=%v", result, err)
				}
				return
			}
			var execution *FilterExecutionError
			if !errors.As(err, &execution) || result != nil {
				t.Fatal("execution failure lost its typed boundary")
			}
			if mode == "error" && !errors.Is(err, failure) {
				t.Fatal("execution error lost its cause")
			}
			if mode == "blocked" {
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("uncooperative filter did not hit its execution deadline: %v", err)
				}
				if elapsed := time.Since(started); elapsed != deadline {
					t.Errorf("execution timed out after %s, want %s", elapsed, deadline)
				}
			}
		}
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, run)
		})
	}
}

func TestSingleFilterConfigurationRefusalStaysDistinct(t *testing.T) {
	failure := errors.New("configuration sentinel")
	registry := NewRegistry()
	registry.Register("fixture", func([]byte) (Filter, error) { return nil, failure })
	engine := NewEngine(registry, slog.New(slog.NewTextHandler(io.Discard, nil)))
	result, err := engine.TestFilter(t.Context(), "fixture", []byte(`{}`), &EmailJSON{})
	var execution *FilterExecutionError
	if result != nil || !errors.Is(err, failure) || errors.As(err, &execution) {
		t.Fatal("configuration refusal was misclassified as execution failure")
	}
}
