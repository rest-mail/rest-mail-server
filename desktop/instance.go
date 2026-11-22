package main

import (
	"os"

	desktopinstance "github.com/rest-mail/rest-mail-server/desktop/internal/instance"
)

// isolatedInstanceEnv names a separate, clearly labelled copy of the app for
// automated testing. It is unset for everyone else, so REST Mail runs once.
const isolatedInstanceEnv = "RESTMAIL_DESKTOP_INSTANCE"

// singleInstanceID names the lock that keeps one copy of REST Mail running.
// Opening the app again, from any location, brings the running window forward.
// An automated test may run a separate copy only by naming it in
// RESTMAIL_DESKTOP_INSTANCE; that copy says so in its window title.
func singleInstanceID(instance string) string {
	if instance = isolatedInstanceName(instance); instance != "" {
		return "com.restmail.desktop.test." + instance
	}
	return "com.restmail.desktop"
}

// isolatedInstanceName is the validated test instance name, or "" when none.
func isolatedInstanceName(instance string) string {
	return desktopinstance.Name(instance)
}

func windowTitle(base, instance string) string {
	if instance = isolatedInstanceName(instance); instance != "" {
		return base + " — test copy " + instance
	}
	return base
}

func currentInstance() string { return os.Getenv(isolatedInstanceEnv) }
