package handlers

import "testing"

// TestBucketQueueStatuses verifies the dashboard maps the outbound_queue status
// values the worker actually writes (delivering/bounced, plus deferred/expired)
// into the Pending/Processing/Failed buckets. The previous mapping looked for
// "processing"/"failed", which are never written, so those counters were always
// zero regardless of queue state.
func TestBucketQueueStatuses(t *testing.T) {
	got := bucketQueueStatuses(map[string]int{
		"pending":    3,
		"deferred":   2,
		"delivering": 4,
		"delivered":  9, // not counted in any bucket
		"bounced":    5,
		"expired":    1,
	})

	if got.Pending != 5 {
		t.Errorf("Pending = %d, want 5 (pending+deferred)", got.Pending)
	}
	if got.Processing != 4 {
		t.Errorf("Processing = %d, want 4 (delivering)", got.Processing)
	}
	if got.Failed != 6 {
		t.Errorf("Failed = %d, want 6 (bounced+expired)", got.Failed)
	}
}

// TestBucketQueueStatuses_NoneWritten guards against a regression to the old
// behaviour: statuses the worker never emits must not leak into any bucket.
func TestBucketQueueStatuses_NoneWritten(t *testing.T) {
	got := bucketQueueStatuses(map[string]int{
		"processing": 7,
		"failed":     7,
		"sent":       7,
	})
	if got != (QueueStats{}) {
		t.Errorf("bucketQueueStatuses(unknown statuses) = %+v, want all zero", got)
	}
}
