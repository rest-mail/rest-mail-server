package queue

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	mtasts "github.com/rest-mail/go-mtasts"
)

// A failed MTA-STS policy refresh is logged at WARN with the domain: delivery
// carries on under the cached policy, and an operator needs to hear before it
// expires (RFC 8461 §3.3, §10.2).
func TestWorker_STSRefreshFailureLogged(t *testing.T) {
	var logs bytes.Buffer
	orig := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(orig) })

	w := NewWorker(nil, "mx.local.test", 1, time.Second)
	txtErr := error(nil)
	w.sts.LookupTXT = func(context.Context, string) ([]string, error) {
		return []string{"v=STSv1; id=20261017"}, txtErr
	}
	w.sts.FetchPolicy = func(context.Context, string) ([]byte, error) {
		return []byte("version: STSv1\nmode: enforce\nmx: mx.remote.test\nmax_age: 86400\n"), nil
	}

	if _, err := w.sts.Resolve(context.Background(), "remote.test"); err != nil {
		t.Fatalf("first resolve: %v", err)
	}
	txtErr = errors.New("SERVFAIL")
	p, err := w.sts.Resolve(context.Background(), "remote.test")
	if err != nil || p.Mode != mtasts.ModeEnforce {
		t.Fatalf("second resolve = %+v, %v; want the cached enforce policy", p, err)
	}

	out := logs.String()
	for _, want := range []string{"level=WARN", "mta-sts: policy refresh failed", "domain=remote.test", "SERVFAIL"} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q:\n%s", want, out)
		}
	}
}
