package main

import (
	"testing"

	"github.com/rest-mail/rest-mail-server/desktop/internal/native"
)

func TestComposeStatusAndFailedCloseCancelQuit(t *testing.T) {
	service := newNative()
	service.SyncCompose("compose-1", ComposeStatus{Dirty: true, CanSave: true, Busy: true})
	if !service.compose["compose-1"].Dirty || !service.compose["compose-1"].CanSave || service.shouldQuit() {
		t.Fatal("busy draft was not protected")
	}
	service.quitting = true
	service.compose["compose-1"].Prompting = true
	service.CancelClose("compose-1")
	if service.quitting || service.compose["compose-1"].Prompting {
		t.Fatal("failed save did not cancel quit")
	}
	service.SyncCompose("compose-1", ComposeStatus{})
	if !service.shouldQuit() {
		t.Fatal("clean draft prevented quit")
	}
	delete(service.compose, "compose-1")
	service.closed["compose-1"] = true
	service.SyncCompose("compose-1", ComposeStatus{Dirty: true})
	if service.compose["compose-1"] != nil {
		t.Fatal("late status revived a closed draft")
	}
}

func TestFileDropsRequireSignedInSession(t *testing.T) {
	service := newNative()
	if _, err := service.DropFiles([]string{"/tmp/private"}); err == nil {
		t.Fatal("signed-out drop accepted")
	}
	if service.RemoveAttachment("missing", "missing") != nil {
		t.Fatal("signed-out attachment exposed")
	}
	service.states["main"] = native.State{SignedIn: true}
	if _, err := service.DropFiles(nil); err == nil {
		t.Fatal("empty drop accepted")
	}
}
