package main

import (
	"reflect"
	"testing"

	"github.com/rest-mail/rest-mail-server/desktop/internal/session"
)

func TestWailsSessionNeverExposesCredentials(t *testing.T) {
	service := reflect.TypeOf(NewSession(session.New()))
	allowed := map[string]bool{"SignIn": true, "SubmitTOTP": true, "SignOut": true, "Status": true, "ServiceStartup": true}
	for index := 0; index < service.NumMethod(); index++ {
		method := service.Method(index)
		if !allowed[method.Name] {
			t.Errorf("unexpected Wails-exposed method: %s", method.Name)
		}
	}
	for _, forbidden := range []string{"SensitiveValues", "Do", "DoRequest", "Redact", "Manager"} {
		if _, found := service.MethodByName(forbidden); found {
			t.Errorf("credential-bearing method bound to Wails: %s", forbidden)
		}
	}
	status := reflect.TypeOf(SessionStatus{})
	for index := 0; index < status.NumField(); index++ {
		if !map[string]bool{"SignedIn": true, "Server": true, "Email": true, "AccountID": true}[status.Field(index).Name] {
			t.Errorf("unexpected session status field: %s", status.Field(index).Name)
		}
	}
}
