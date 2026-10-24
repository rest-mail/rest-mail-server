package dkimrotate

import "time"

// SetWorkerClock gives a test's worker the clock it steps through a rotation with.
func SetWorkerClock(w *Worker, now func() time.Time) { w.now = now }
