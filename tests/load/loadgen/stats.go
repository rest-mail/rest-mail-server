package main

import (
	"encoding/json"
	"math"
	"os"
	"sort"
	"sync"
	"time"
)

// maxErrorKinds caps how many distinct error messages a result keeps, so a run
// where every failure carries a unique message cannot grow the file unbounded.
const maxErrorKinds = 20

// recorder collects the outcome of every operation one generator makes.
type recorder struct {
	mu        sync.Mutex
	latencies []time.Duration
	ok        int64
	failed    int64
	errors    map[string]int64
}

func newRecorder() *recorder {
	return &recorder{errors: map[string]int64{}}
}

// record adds one operation. A nil err is a success, timed by d.
func (r *recorder) record(d time.Duration, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err != nil {
		r.failed++
		msg := err.Error()
		if len(msg) > 200 {
			msg = msg[:200]
		}
		if _, seen := r.errors[msg]; seen || len(r.errors) < maxErrorKinds {
			r.errors[msg]++
		} else {
			r.errors["(other errors)"]++
		}
		return
	}
	r.ok++
	r.latencies = append(r.latencies, d)
}

// Latency is a set of percentiles in milliseconds.
type Latency struct {
	P50 float64 `json:"p50"`
	P90 float64 `json:"p90"`
	P95 float64 `json:"p95"`
	P99 float64 `json:"p99"`
	Max float64 `json:"max"`
}

// Result is what one generator writes: the shape every kind shares, so the
// runner and the compare script read them the same way.
type Result struct {
	Kind         string           `json:"kind"`
	Target       float64          `json:"target"`
	TargetUnit   string           `json:"target_unit"`
	DurationSec  float64          `json:"duration_s"`
	Attempted    int64            `json:"attempted"`
	OK           int64            `json:"ok"`
	Errors       int64            `json:"errors"`
	AchievedRate float64          `json:"achieved_rate"`
	LatencyMS    Latency          `json:"latency_ms"`
	ErrorKinds   map[string]int64 `json:"error_kinds,omitempty"`
	Extra        map[string]any   `json:"extra,omitempty"`
}

func (r *recorder) result(kind string, target float64, unit string, elapsed time.Duration) Result {
	r.mu.Lock()
	defer r.mu.Unlock()
	res := Result{
		Kind:        kind,
		Target:      target,
		TargetUnit:  unit,
		DurationSec: round(elapsed.Seconds()),
		Attempted:   r.ok + r.failed,
		OK:          r.ok,
		Errors:      r.failed,
		LatencyMS:   percentiles(r.latencies),
	}
	if elapsed > 0 {
		res.AchievedRate = round(float64(r.ok) / elapsed.Seconds())
	}
	if len(r.errors) > 0 {
		res.ErrorKinds = map[string]int64{}
		for k, v := range r.errors {
			res.ErrorKinds[k] = v
		}
	}
	return res
}

// percentiles uses the nearest-rank method on a sorted copy.
func percentiles(in []time.Duration) Latency {
	if len(in) == 0 {
		return Latency{}
	}
	d := append([]time.Duration(nil), in...)
	sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
	at := func(p float64) float64 {
		rank := int(math.Ceil(p/100*float64(len(d)))) - 1
		if rank < 0 {
			rank = 0
		}
		return ms(d[rank])
	}
	return Latency{P50: at(50), P90: at(90), P95: at(95), P99: at(99), Max: ms(d[len(d)-1])}
}

func ms(d time.Duration) float64 { return round(float64(d) / float64(time.Millisecond)) }

func round(f float64) float64 { return math.Round(f*1000) / 1000 }

func writeResult(path string, res Result) error {
	b, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if path == "" || path == "-" {
		_, err = os.Stdout.Write(b)
		return err
	}
	return os.WriteFile(path, b, 0o644)
}
