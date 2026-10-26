package api

import (
	"database/sql/driver"
	"sync"
)

type bulkQueueExampleMutation struct {
	query string
	args  []driver.NamedValue
}

type bulkQueueExampleStore struct {
	mu    sync.Mutex
	calls []bulkQueueExampleMutation
}

func (s *bulkQueueExampleStore) snapshot() []bulkQueueExampleMutation {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]bulkQueueExampleMutation(nil), s.calls...)
}

type bulkQueueExampleTx struct{}

func (bulkQueueExampleTx) Commit() error { return nil }

func (bulkQueueExampleTx) Rollback() error { return nil }

type bulkQueueExampleScenario struct {
	accountReadScenario
	store *bulkQueueExampleStore
}
