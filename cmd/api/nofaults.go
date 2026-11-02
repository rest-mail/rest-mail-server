//go:build !faultinject

package main

import "net/http"

// injectFaults is the normal build's: no fault, the handler as it is. The
// faultinject build's is in faults.go.
func injectFaults(h http.Handler) http.Handler {
	return h
}
