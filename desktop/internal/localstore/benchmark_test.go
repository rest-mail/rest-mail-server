package localstore

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	apitypes "github.com/rest-mail/rest-mail-server/pkg/apitypes"
)

func BenchmarkSyntheticCorpus(benchmark *testing.B) {
	if os.Getenv("RESTMAIL_DESKTOP_BENCHMARK") != "1" {
		benchmark.Skip("set RESTMAIL_DESKTOP_BENCHMARK=1 for the 200k-message / 500+ MB synthetic corpus")
	}
	root := benchmark.TempDir()
	store, err := Open(root, AccountKey("synthetic.invalid", "benchmark", 1))
	if err != nil {
		benchmark.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if err = store.PutFolders(ctx, []apitypes.FolderSummary{{Folder: "INBOX"}}); err != nil {
		benchmark.Fatal(err)
	}
	_, generation, err := store.BeginScan(ctx, "INBOX")
	if err != nil {
		benchmark.Fatal(err)
	}
	count := 200000
	var corpusBytes int64
	started := time.Now()
	random := rand.New(rand.NewPCG(42, 91))
	words := []string{"invoice", "meeting", "delivery", "project", "schedule", "review", "release", "travel", "receipt", "design", "report", "customer", "archive", "planning", "budget", "engineering", "calendar", "summary", "attachment", "discussion", "proposal", "request", "update", "tomorrow", "afternoon", "morning", "progress", "support", "office", "document", "agreement", "shipment", "coffee", "research", "conference", "software", "hardware", "network", "security", "performance", "message", "reference", "November", "November2026", "synthetic", "testing", "workshop", "reservation", "résumé", "café", "Bücher", "thanks", "regards", "confirm", "important", "followup", "details", "notes", "hello", "team", "available", "complete", "status", "results"}
	for offset := 0; offset < count; offset += 500 {
		messages := make([]apitypes.Message, 0, 500)
		for index := offset; index < offset+500; index++ {
			var body strings.Builder
			fmt.Fprintf(&body, "Synthetic project%d tracking%d. ", index%1000, index)
			for body.Len() < 2800 {
				body.WriteString(words[random.IntN(len(words))])
				body.WriteByte(' ')
			}
			message := apitypes.Message{ID: uint(index + 1), MailboxID: 1, Folder: "INBOX", ThreadID: fmt.Sprintf("thread-%d", index/3), Subject: fmt.Sprintf("Project%d progress report %d", index%1000, index), Sender: fmt.Sprintf("sender%d@xn--bcher-kva.test", index%10000), SenderName: "José Synthetic", RecipientsTo: json.RawMessage(`["reader@example.test"]`), RecipientsCc: json.RawMessage(`["team@example.test"]`), BodyText: body.String(), SizeBytes: body.Len(), ReceivedAt: time.Date(2026, 11, 1+index%21, 12, index%60, 0, 0, time.UTC), IsRead: index%3 != 0, HasAttachments: index%7 == 0}
			corpusBytes += int64(len(message.BodyText))
			messages = append(messages, message)
		}
		if err = store.PutBatch(ctx, messages, true, generation); err != nil {
			benchmark.Fatal(err)
		}
	}
	if err = store.SavePage(ctx, "INBOX", "", generation, 1, nil, true); err != nil {
		benchmark.Fatal(err)
	}
	backfill := time.Since(started)
	if corpusBytes < 500000000 {
		benchmark.Fatal("corpus too small")
	}
	if _, err = store.writer.Exec(`INSERT INTO message_fts(message_fts) VALUES('optimize'); PRAGMA wal_checkpoint(TRUNCATE);`); err != nil {
		benchmark.Fatal(err)
	}
	queries := []string{"project42", "subject:project17", "from:sender142@bücher.test", "has:attachment subject:project99", "is:unread project243", "after:2026-11-10 before:2026-11-15 project723", "in:INBOX project501", "tracking199999"}
	for _, query := range queries {
		if _, err = store.Search(ctx, query, "", 50, 0); err != nil {
			benchmark.Fatal(err)
		}
	}
	latencies := make([]time.Duration, 0, 400)
	benchmark.ResetTimer()
	for iteration := 0; iteration < 400; iteration++ {
		started := time.Now()
		if _, err = store.Search(ctx, queries[iteration%len(queries)], "", 50, 0); err != nil {
			benchmark.Fatal(err)
		}
		latencies = append(latencies, time.Since(started))
	}
	benchmark.StopTimer()
	sort.Slice(latencies, func(left, right int) bool { return latencies[left] < latencies[right] })
	status, err := store.Status(ctx)
	if err != nil {
		benchmark.Fatal(err)
	}
	p95 := latencies[(len(latencies)*95/100)-1]
	var ftsBytes int64
	if err = store.reader.QueryRow(`SELECT coalesce(sum(pgsize),0) FROM dbstat WHERE name LIKE 'message_fts_%'`).Scan(&ftsBytes); err != nil {
		benchmark.Fatal(err)
	}
	benchmark.ReportMetric(float64(corpusBytes)/1e6, "corpus_MB")
	benchmark.ReportMetric(float64(count), "messages")
	benchmark.ReportMetric(float64(status.Bytes)/1e6, "store_MB")
	benchmark.ReportMetric(float64(ftsBytes)/1e6, "fts_MB")
	benchmark.ReportMetric(float64(p95.Microseconds())/1000, "p95_ms")
	benchmark.ReportMetric(float64(count)/backfill.Seconds(), "messages/s")
	benchmark.ReportMetric(float64(corpusBytes)/1e6/backfill.Seconds(), "MB/s")
	benchmark.Logf("%s/%s; generated %d messages, %d body bytes; backfill=%s; store=%d bytes; p95=%s; queries=%q", runtime.GOOS, runtime.GOARCH, count, corpusBytes, backfill, status.Bytes, p95, queries)
}
