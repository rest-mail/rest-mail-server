package main

import (
	"log"

	"github.com/rest-mail/rest-mail-server/desktop/internal/native"
)

// maxPendingMailto bounds the links held while the app starts or is signed
// out. Older links are dropped first.
const maxPendingMailto = 16

// openMailto queues a mailto: link from the operating system, brings the main
// window forward and tells the frontend to take it. Links stay queued until a
// signed-in main window takes them, so a link that starts the app is not lost.
func (service *Native) openMailto(link string) {
	message, err := native.ParseMailto(link)
	if err != nil {
		log.Printf("Ignored mailto: link: %v", err)
		return
	}
	service.mutex.Lock()
	service.mailto = append(service.mailto, message)
	if len(service.mailto) > maxPendingMailto {
		service.mailto = service.mailto[len(service.mailto)-maxPendingMailto:]
	}
	waiting := len(service.mailto)
	app := service.app
	service.mutex.Unlock()
	log.Printf("Queued a mailto: link (%d waiting)", waiting)
	if app == nil {
		return
	}
	if window, exists := app.Window.GetByName("main"); exists {
		window.Show().Restore()
		window.Focus()
	}
	app.Event.Emit("native:mailto")
}

// TakeMailto returns the queued mailto: messages and clears the queue. It
// returns nothing while signed out, so the links wait for sign-in.
func (service *Native) TakeMailto() []native.Mailto {
	service.mutex.Lock()
	defer service.mutex.Unlock()
	if !service.states["main"].SignedIn || len(service.mailto) == 0 {
		return []native.Mailto{}
	}
	pending := service.mailto
	service.mailto = nil
	return pending
}
