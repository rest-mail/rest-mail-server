package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/config"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestAdminQueueAndSnapshotSelectMetadataColumns(t *testing.T) {
	db, err := gorm.Open(postgres.Open("host=127.0.0.1 port=1 user=unused dbname=unused sslmode=disable"), &gorm.Config{
		DryRun: true, DisableAutomaticPing: true, SkipDefaultTransaction: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	var statements map[string][]string
	err = db.Callback().Query().After("gorm:query").Register("test:admin-source-columns", func(tx *gorm.DB) {
		stmt := tx.Statement
		switch stmt.Table {
		case "messages", "outbound_queue":
			sql := stmt.SQL.String()
			if !strings.HasPrefix(sql, "SELECT count(*)") {
				statements[stmt.Table] = append(statements[stmt.Table], sql)
			}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	queue := &QueueHandler{db: db}
	logs := &LogHandler{db: db}
	stats := &StatsHandler{db: db}
	test := NewTestHandler(db, &config.Config{Environment: "test"})
	for _, tc := range []struct {
		name     string
		query    func() error
		snapshot bool
	}{
		{"queue list", func() error { _, err := queue.listQueue(r, listQueueRequest{}); return err }, false},
		{"queue detail", func() error { _, err := queue.getQueueEntry(r, queuePath{ID: 1}); return err }, false},
		{"delivery log", func() error { _, err := logs.deliveryLog(r, deliveryLogRequest{}); return err }, false},
		{"recent activity", func() error { _, err := stats.getRecentActivity(); return err }, false},
		{"test snapshot", func() error { _, err := test.snapshot(r, endpoint.Empty{}); return err }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			statements = make(map[string][]string)
			if err := tc.query(); err != nil {
				t.Fatal(err)
			}
			tables := []string{"outbound_queue"}
			if tc.snapshot {
				tables = append(tables, "messages")
			}
			for _, table := range tables {
				sql := statements[table]
				if len(sql) != 1 {
					t.Fatalf("expected one read from %s, got %v", table, sql)
				}
				selected, _, _ := strings.Cut(sql[0], " FROM ")
				for _, unwanted := range []string{"*", "raw_message", "raw_octets", "source_ref", "source_header", "header_metadata"} {
					if strings.Contains(selected, unwanted) {
						t.Fatalf("selects %s: %s", unwanted, sql[0])
					}
				}
			}
		})
	}
}
