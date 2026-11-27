package filters

import (
	"context"
	"encoding/json"
	"errors"
	"mime"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/pipeline"
)

// longSieve is a script and message that take seconds to evaluate: a few hundred
// address tests, each reading a To list of tens of thousands of addresses, none of
// which matches.
func longSieve(t *testing.T) (pipeline.Filter, *pipeline.EmailJSON) {
	t.Helper()
	const addresses, tests = 1 << 16, 400
	email := &pipeline.EmailJSON{}
	email.Headers.Subject = "s"
	email.Headers.To = pipeline.ParsedAddresses(strings.Repeat("a@b.test,", addresses-1)+"a@b.test", &mime.WordDecoder{})
	email.Envelope.RcptTo = []string{"owner@b.test"}
	script := strings.Repeat("if address :is \"to\" \"nobody@example.test\" { keep; }\n", tests)
	cfg, err := json.Marshal(map[string]string{"script": script})
	if err != nil {
		t.Fatal(err)
	}
	f, err := NewSieve(cfg)
	if err != nil {
		t.Fatalf("NewSieve: %v", err)
	}
	return f, email
}

// A Sieve evaluation stops soon after its context ends, rather than running to the
// end of the script: Execute returns the context's error, not the fail-safe keep,
// and the goroutine running it is done (#2107). go-sieve takes no context, so this
// is the adapter's checks, here the one before each address a test reads.
func TestSieveStopsWhenItsContextEnds(t *testing.T) {
	f, email := longSieve(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	type outcome struct {
		res *pipeline.FilterResult
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := f.Execute(ctx, email)
		done <- outcome{res, err}
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()
	cancelled := time.Now()

	select {
	case o := <-done:
		if waited := time.Since(cancelled); waited > 250*time.Millisecond {
			t.Errorf("evaluation went on %s after its context ended", waited)
		}
		if !errors.Is(o.err, context.Canceled) {
			t.Fatalf("Execute = %+v, %v; want the context's error", o.res, o.err)
		}
		if o.res != nil {
			t.Fatalf("a stopped evaluation returned a result: %+v", o.res)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("evaluation did not stop when its context ended")
	}
}

// An evaluation whose context has already ended does not start.
func TestSieveNotEvaluatedAfterItsContextEnded(t *testing.T) {
	f, email := longSieve(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	res, err := f.Execute(ctx, email)
	if !errors.Is(err, context.Canceled) || res != nil {
		t.Fatalf("Execute = %+v, %v; want the context's error", res, err)
	}
	if took := time.Since(start); took > 100*time.Millisecond {
		t.Errorf("Execute took %s on an ended context", took)
	}
}
