package imap

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/gateway/apiclient"
)

func zoneSearchTranscript(t *testing.T, api *fakeAPI) *transcript {
	t.Helper()
	srv := httptest.NewServer(api.handler())
	t.Cleanup(srv.Close)
	h := newTranscript(t, NewBackend(apiclient.New(srv.URL), nil))
	h.login("a1")
	if _, st := h.command("a2", "SELECT INBOX"); !strings.Contains(st, "OK") {
		t.Fatalf("SELECT status = %q", st)
	}
	return h
}

func requireZoneSearch(t *testing.T, h *transcript, criteria, want string) {
	t.Helper()
	unt, st := h.command("s", "SEARCH %s", criteria)
	if !strings.Contains(st, "OK") || len(unt) != 1 || strings.TrimSpace(unt[0]) != want {
		t.Errorf("SEARCH %s = %q / %q, want %q", criteria, unt, st, want)
	}
}

// TestSearchZoneFollowsTheAccount proves the gateway reads the account's zone for a
// SEARCH with an internal-date key, uses UTC when it is null, and sees a change on
// a connection that is already open.
func TestSearchZoneFollowsTheAccount(t *testing.T) {
	api := newFakeAPI()
	api.seed(100, "INBOX", "Subject: Late\r\n\r\nhi\r\n", "Late")
	// 03:00 UTC on 15 March is still 14 March in New York.
	api.msgs[100].receivedAt = time.Date(2026, 3, 15, 3, 0, 0, 0, time.UTC)
	h := zoneSearchTranscript(t, api)

	requireZoneSearch(t, h, "ON 15-Mar-2026", "* SEARCH 1")
	requireZoneSearch(t, h, "ON 14-Mar-2026", "* SEARCH")

	zone := "America/New_York"
	api.mu.Lock()
	api.timeZone = &zone
	api.mu.Unlock()
	requireZoneSearch(t, h, "ON 14-Mar-2026", "* SEARCH 1")
	requireZoneSearch(t, h, "ON 15-Mar-2026", "* SEARCH")

	api.mu.Lock()
	reads := api.timeZoneReads
	api.mu.Unlock()
	requireZoneSearch(t, h, "SUBJECT Late", "* SEARCH 1")
	api.mu.Lock()
	defer api.mu.Unlock()
	if api.timeZoneReads != reads {
		t.Errorf("a SEARCH with no internal-date key read the zone: %d reads, want %d", api.timeZoneReads, reads)
	}
}

// TestSearchZoneUnreadableAnswersNO proves a zone the gateway cannot read or load
// answers NO rather than searching some other zone's days, and that the session
// goes on working.
func TestSearchZoneUnreadableAnswersNO(t *testing.T) {
	for name, setup := range map[string]func(*fakeAPI){
		"the API fails":         func(api *fakeAPI) { api.timeZoneFails = true },
		"the name is not known": func(api *fakeAPI) { zone := "Mars/Olympus"; api.timeZone = &zone },
	} {
		t.Run(name, func(t *testing.T) {
			api := newFakeAPI()
			api.seed(100, "INBOX", "Subject: Late\r\n\r\nhi\r\n", "Late")
			setup(api)
			h := zoneSearchTranscript(t, api)
			unt, st := h.command("s", "SEARCH ON 15-Mar-2025")
			if len(unt) != 0 || !strings.Contains(st, "s NO [UNAVAILABLE]") {
				t.Errorf("SEARCH = %q / %q, want a tagged NO [UNAVAILABLE] alone", unt, st)
			}
			requireZoneSearch(t, h, "SUBJECT Late", "* SEARCH 1")
		})
	}
}
