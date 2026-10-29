//go:build !faultinject

package main

import (
	"net/http"
	"testing"
)

// A normal build serves the router itself, with no fault in front of it.
func TestANormalBuildInjectsNoFault(t *testing.T) {
	router := http.NewServeMux()
	if got := injectFaults(router); got != http.Handler(router) {
		t.Fatal("a build without the faultinject tag wrapped the public handler")
	}
}
