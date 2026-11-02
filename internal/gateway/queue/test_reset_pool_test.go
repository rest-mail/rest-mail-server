package queue

import (
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/config"
)

func TestTestResetWorkerSingleConnectionPool(t *testing.T) {
	database := openResetWorkerDB(t)
	pool, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { pool.SetMaxOpenConns(0) })
	worker := NewWorker(database, "reset.test", 1, time.Second)
	worker.EnableTestReset(&config.Config{Environment: "test"})
	finished := make(chan struct{})
	go func() { worker.processOne(0); close(finished) }()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		pool.SetMaxOpenConns(2)
		t.Fatal("reset-enabled worker deadlocked a single-connection pool")
	}
}
