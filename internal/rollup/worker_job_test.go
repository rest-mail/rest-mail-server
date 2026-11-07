package rollup

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/requestid"
	"github.com/rest-mail/rest-mail-server/internal/tracing"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// unreachableDB is a database every query to fails: nothing listens on its
// port, and nothing connects until a query runs.
func unreachableDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(postgres.Open("host=127.0.0.1 port=1 user=x dbname=x sslmode=disable connect_timeout=1"),
		&gorm.Config{DisableAutomaticPing: true, Logger: logger.Discard})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return db
}

// A line the snapshot writes from inside rollupOnce carries the ID of the job
// that ran it (#1580), as the job's own lines do.
func TestRollupOnce_LinesCarryTheJobID(t *testing.T) {
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(requestid.NewHandler(slog.NewJSONHandler(&logs, nil))))
	t.Cleanup(func() { slog.SetDefault(prev) })

	reg, cv := terminalCounter()
	cv.WithLabelValues("inbound", "delivered").Add(1)
	w := NewWorker(unreachableDB(t), 5*time.Minute)
	w.gatherer = reg

	var id string
	_ = tracing.Job(context.Background(), "job.rollup_snapshot", func(ctx context.Context) error {
		id = requestid.FromContext(ctx)
		return w.rollupOnce(ctx, time.Now())
	})
	if !strings.HasPrefix(id, tracing.JobPrefix) {
		t.Fatalf("job ID = %q", id)
	}

	found := false
	for sc := bufio.NewScanner(&logs); sc.Scan(); {
		var line map[string]any
		if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
			t.Fatalf("log line %q: %v", sc.Text(), err)
		}
		if line["msg"] != "rollup worker: upsert failed" {
			continue
		}
		found = true
		if line[requestid.Attr] != id {
			t.Errorf("the snapshot's line has %s %v, want the job's %q", requestid.Attr, line[requestid.Attr], id)
		}
	}
	if !found {
		t.Fatalf("rollupOnce wrote no upsert failure line: %s", logs.String())
	}
}
