package push

import (
	"context"
	"sync"
	"testing"
	"time"
)

// fakeStore has one device, 1, for account 3 of mailbox 7.
type fakeStore struct {
	mu   sync.Mutex
	gone []uint
}

func (s *fakeStore) targets(mailboxID uint, _ time.Time) ([]target, error) {
	if mailboxID != 7 {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.gone) > 0 {
		return nil, nil
	}
	return []target{{DeviceID: 1, Token: testToken, Environment: "production", Account: 3}}, nil
}

func (s *fakeStore) unread(uint) (int64, error) { return 2, nil }

func (s *fakeStore) RemoveGone(id uint, _ string, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gone = append(s.gone, id)
	return nil
}

func (s *fakeStore) Sweep(time.Time) (int, error) { return 0, nil }

func (s *fakeStore) removed() []uint {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]uint(nil), s.gone...)
}

// fakeSender hands each push to the test, and answers with result.
type fakeSender struct {
	sent   chan Notification
	result Result
}

func (s *fakeSender) Send(_ context.Context, n Notification) (Result, error) {
	s.sent <- n
	return s.result, nil
}

func waitPush(t *testing.T, sent chan Notification) Notification {
	t.Helper()
	select {
	case n := <-sent:
		return n
	case <-time.After(5 * time.Second):
		t.Fatal("no push within 5s")
		return Notification{}
	}
}

// TestDispatcherWorker: the worker pushes new mail at once, tells mail
// inside the window in one push at its end, and stops on Close.
func TestDispatcherWorker(t *testing.T) {
	sender := &fakeSender{sent: make(chan Notification, 8), result: Result{Status: 200}}
	d := newDispatcher(&fakeStore{}, sender, 200*time.Millisecond)
	d.Start()

	d.NewMail(7)
	if n := waitPush(t, sender.sent); string(n.Payload) != string(Payload("1", 3, 2)) || n.Token != testToken {
		t.Fatalf("push = %s to %s; want the signal for device 1, account 3", n.Payload, n.Token)
	}
	sentAt := time.Now()
	d.NewMail(7)
	d.NewMail(7)
	waitPush(t, sender.sent)
	if waited := time.Since(sentAt); waited < 150*time.Millisecond {
		t.Errorf("the second push came %v after the first, inside the window", waited)
	}
	select {
	case n := <-sender.sent:
		t.Fatalf("a third push, %s, for mail told in the second", n.Payload)
	case <-time.After(400 * time.Millisecond):
	}
	d.NewMail(8) // no devices: nothing
	closed := make(chan struct{})
	go func() { d.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not stop the worker")
	}
}

// TestDispatcherRemovesGoneDevices: a device APNs says is gone is removed.
func TestDispatcherRemovesGoneDevices(t *testing.T) {
	store := &fakeStore{}
	sender := &fakeSender{sent: make(chan Notification, 1), result: Result{Status: 410, Reason: "Unregistered"}}
	d := newDispatcher(store, sender, time.Second)
	d.NewMail(7)
	d.Step(context.Background(), time.Now())
	<-sender.sent
	if got := store.removed(); len(got) != 1 || got[0] != 1 {
		t.Fatalf("removed %v, want device 1", got)
	}
}

// TestDispatcherCloseWithoutStart: closing a dispatcher never started
// returns at once.
func TestDispatcherCloseWithoutStart(t *testing.T) {
	d := newDispatcher(&fakeStore{}, &fakeSender{}, 0)
	done := make(chan struct{})
	go func() { d.Close(); d.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Close of a dispatcher never started blocked")
	}
}
