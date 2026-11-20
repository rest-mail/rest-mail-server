package main

import (
	"fmt"
	"net/http"
	"regexp"
)

// fakeExportSession is the export session a fake server opens.
const fakeExportSession = "00112233445566778899aabbccddeeff"

var exportSessionRoute = regexp.MustCompile(`^/api/v1/accounts/[0-9]+/exports(/[0-9a-f]{32}/close)?$`)

// withExportSessions answers the routes that open and close an audited export
// as the server does when the key holds mail:export, and passes every other
// request to next: the fake servers of the export tests serve the data.
func withExportSessions(next func(http.ResponseWriter, *http.Request)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		match := exportSessionRoute.FindStringSubmatch(r.URL.Path)
		if r.Method != http.MethodPost || match == nil {
			next(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if match[1] == "" {
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprintf(w, `{"data":{"id":%q}}`, fakeExportSession)
			return
		}
		_, _ = fmt.Fprint(w, `{"data":{}}`)
	})
}
