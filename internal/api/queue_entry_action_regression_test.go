package api

import (
	"sync/atomic"
)

type queueEntryActionScenario struct {
	bulkQueueExampleScenario
	reads *atomic.Int64
}
