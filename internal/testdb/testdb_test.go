package testdb

import (
	"runtime"
	"testing"
)

// recorder keeps which of Fatalf and Skipf ended the test. Both really end a
// test by stopping its goroutine, so the check runs in one of its own.
type recorder struct {
	testing.TB
	ended string
}

func (r *recorder) Helper() {}
func (r *recorder) Fatalf(string, ...any) {
	r.ended = "fatal"
	runtime.Goexit()
}
func (r *recorder) Skipf(string, ...any) {
	r.ended = "skip"
	runtime.Goexit()
}

func TestUnreachable(t *testing.T) {
	for _, tc := range []struct{ host, want string }{
		{"", "skip"},
		{"localhost", "fatal"},
	} {
		t.Run("DB_HOST="+tc.host, func(t *testing.T) {
			t.Setenv("DB_HOST", tc.host)
			r := &recorder{TB: t}
			done := make(chan struct{})
			go func() {
				defer close(done)
				Unreachable(r, "no database reachable")
			}()
			<-done
			if r.ended != tc.want {
				t.Fatalf("ended with %q, want %q", r.ended, tc.want)
			}
		})
	}
}
