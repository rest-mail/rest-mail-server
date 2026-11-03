package server

import (
	"slices"
	"sync"
	"testing"
	"time"
)

// observingBackend is a mockBackend that implements imap.CommandObserver.
type observingBackend struct {
	*mockBackend
	mu    sync.Mutex
	names []string
}

func (b *observingBackend) ObserveCommand(name string, d time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if d < 0 {
		name += "(negative)"
	}
	b.names = append(b.names, name)
}

func (b *observingBackend) observed() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.names)
}

// TestCommandObserver_SeesEachCommand: a backend implementing
// imap.CommandObserver hears of every command the session handles, by its
// upper-cased name, an unknown one included, once each and in order.
func TestCommandObserver_SeesEachCommand(t *testing.T) {
	mock := newMockBackend()
	back := &observingBackend{mockBackend: mock}
	h := newIMAPHarnessWith(t, back, mock.user, mock.pass)

	h.command("a1", "capability")
	h.login("a2")
	h.command("a3", "NOOP")
	h.command("a4", "BOGUS")
	// The session observes a command after its tagged reply; this last
	// command's reply means every earlier one has been observed.
	h.command("a5", "NOOP")

	got := back.observed()
	want := []string{"CAPABILITY", "LOGIN", "NOOP", "BOGUS"}
	if len(got) < len(want) || !slices.Equal(got[:len(want)], want) {
		t.Fatalf("observed %v, want %v first", got, want)
	}
}
