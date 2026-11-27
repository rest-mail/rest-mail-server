package main

import (
	"fmt"
	"testing"

	"github.com/rest-mail/rest-mail-server/desktop/internal/native"
)

func TestMailtoLinksWaitForSignInAndAreTakenOnce(t *testing.T) {
	service := newNative()
	service.openMailto("mailto:a@example.com?subject=Hello")
	service.openMailto("mailto:a@example.com?subject=Hi%0D%0ABcc:%20victim@example.com")
	if got := service.TakeMailto(); len(got) != 0 {
		t.Fatalf("signed-out TakeMailto returned %+v", got)
	}
	service.states["main"] = native.State{SignedIn: true}
	got := service.TakeMailto()
	if len(got) != 1 || got[0] != (native.Mailto{To: "a@example.com", Subject: "Hello"}) {
		t.Fatalf("TakeMailto = %+v, want only the safe link", got)
	}
	if again := service.TakeMailto(); len(again) != 0 {
		t.Fatalf("second TakeMailto returned %+v", again)
	}
}

func TestMailtoQueueKeepsTheNewestLinks(t *testing.T) {
	service := newNative()
	for index := range maxPendingMailto + 4 {
		service.openMailto(fmt.Sprintf("mailto:a@example.com?subject=%d", index))
	}
	service.states["main"] = native.State{SignedIn: true}
	got := service.TakeMailto()
	if len(got) != maxPendingMailto || got[0].Subject != "4" || got[len(got)-1].Subject != fmt.Sprint(maxPendingMailto+3) {
		t.Fatalf("TakeMailto kept %d links from %q to %q", len(got), got[0].Subject, got[len(got)-1].Subject)
	}
}
