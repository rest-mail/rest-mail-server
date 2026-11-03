package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/quiethours"
)

func utc(text string) time.Time {
	at, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		panic(err)
	}
	return at
}

// The boundaries of a day in a zone, from the zone's own clock changes: a day of 23 or 25
// hours, and a midnight a clock change skips or repeats.
func TestDayBoundariesFollowTheZone(t *testing.T) {
	for _, c := range []struct{ zone, day, start, end string }{
		{"UTC", "2026-03-08", "2026-03-08T00:00:00Z", "2026-03-08T23:59:59.999999Z"},
		// New York: a 23-hour day as the clocks go forward, a 25-hour day as they go back.
		{"America/New_York", "2026-03-08", "2026-03-08T05:00:00Z", "2026-03-09T03:59:59.999999Z"},
		{"America/New_York", "2026-11-01", "2026-11-01T04:00:00Z", "2026-11-02T04:59:59.999999Z"},
		// Ahead of UTC, the zone's day starts on UTC's previous day.
		{"Asia/Tokyo", "2026-03-08", "2026-03-07T15:00:00Z", "2026-03-08T14:59:59.999999Z"},
		// Havana skips midnight: its day starts at 01:00, an hour later than the offset before.
		{"America/Havana", "2026-03-08", "2026-03-08T05:00:00Z", "2026-03-09T03:59:59.999999Z"},
		// Havana repeats midnight: its day starts at the first of the two.
		{"America/Havana", "2026-11-01", "2026-11-01T04:00:00Z", "2026-11-02T04:59:59.999999Z"},
		// Apia skipped 30 December 2011 altogether: nothing is in that day.
		{"Pacific/Apia", "2011-12-30", "2011-12-30T10:00:00Z", "2011-12-30T09:59:59.999999Z"},
	} {
		t.Run(c.zone+"/"+c.day, func(t *testing.T) {
			zone, err := quiethours.Location(c.zone)
			if err != nil {
				t.Fatal(err)
			}
			day, err := time.Parse(time.DateOnly, c.day)
			if err != nil {
				t.Fatal(err)
			}
			if got := dayStart(day, zone); !got.Equal(utc(c.start)) {
				t.Errorf("start %s, want %s", got.UTC().Format(time.RFC3339Nano), c.start)
			}
			if got := dayEnd(day, zone); !got.Equal(utc(c.end)) {
				t.Errorf("end %s, want %s", got.UTC().Format(time.RFC3339Nano), c.end)
			}
		})
	}
}

// searchTimeZoneFixture is an account with a message at each of the given instants, each
// under its own instant as its subject.
func searchTimeZoneFixture(t *testing.T, instants ...string) (*SearchHandler, msgDeleteFixture) {
	t.Helper()
	db, err := connectTestDB()
	if err != nil {
		t.Skipf("no test database: %v", err)
	}
	if err := db.AutoMigrate(append([]interface{}{&models.Message{}}, accountModels...)...); err != nil {
		t.Fatal(err)
	}
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	fx := seedMsgDeleteFixture(t, tx, 0)
	for i, instant := range instants {
		message := models.Message{MailboxID: fx.mailbox.ID, Folder: "INBOX", Subject: instant,
			MsgID: fmt.Sprintf("<zone-%d@example.net>", i), Sender: "sender@example.net", ReceivedAt: utc(instant)}
		if err := tx.Create(&message).Error; err != nil {
			t.Fatalf("seed the message at %s: %v", instant, err)
		}
	}
	return NewSearchHandler(tx), fx
}

// searchFor sends the query through the route, its binding included.
func searchFor(h *SearchHandler, fx msgDeleteFixture, query url.Values) *httptest.ResponseRecorder {
	path := fmt.Sprintf("/api/v1/accounts/%d/search?%s", fx.mailbox.ID, query.Encode())
	req := authzRequest(http.MethodGet, path, fx.mailbox.ID, nil, "")
	rr := httptest.NewRecorder()
	searchRoutes(h).ServeHTTP(rr, req)
	return rr
}

// foundInstants is the subjects a 200 search found, which are the instants it was seeded with.
func foundInstants(t *testing.T, rr *httptest.ResponseRecorder) []string {
	t.Helper()
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rr.Code, rr.Body.String())
	}
	var body struct {
		Data []struct {
			Subject string `json:"subject"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	found := []string{}
	for _, message := range body.Data {
		found = append(found, message.Subject)
	}
	sort.Strings(found)
	return found
}

func TestSearchCalendarDaysInAZone(t *testing.T) {
	h, fx := searchTimeZoneFixture(t,
		"2026-03-08T04:59:59Z",        // New York 7 March 23:59:59, UTC's 8 March
		"2026-03-08T05:00:00Z",        // New York 8 March 00:00:00, the day's first instant
		"2026-03-09T03:30:00Z",        // New York 8 March 23:30, UTC's 9 March
		"2026-03-09T03:59:59.999999Z", // New York 8 March's last instant
		"2026-03-09T04:00:00Z",        // New York 9 March 00:00:00, in daylight time
		"2026-03-09T04:30:00Z",        // New York 9 March 00:30, though five hours back says 8 March
		"2026-11-01T03:59:59Z",        // New York 31 October 23:59:59, UTC's 1 November
		"2026-11-01T04:00:00Z",        // New York 1 November 00:00:00, in daylight time
		"2026-11-02T04:30:00Z",        // New York 1 November 23:30, in standard time, UTC's 2 November
		"2026-11-02T05:00:00Z",        // New York 2 November 00:00:00
	)
	for _, c := range []struct {
		name          string
		after, before string
		zone          string
		want          []string
	}{
		{"a 23-hour day in New York", "2026-03-08", "2026-03-08", "America/New_York",
			[]string{"2026-03-08T05:00:00Z", "2026-03-09T03:30:00Z", "2026-03-09T03:59:59.999999Z"}},
		{"a 25-hour day in New York", "2026-11-01", "2026-11-01", "America/New_York",
			[]string{"2026-11-01T04:00:00Z", "2026-11-02T04:30:00Z"}},
		{"the same day in UTC disagrees", "2026-03-08", "2026-03-08", "UTC",
			[]string{"2026-03-08T04:59:59Z", "2026-03-08T05:00:00Z"}},
		{"only a start", "2026-11-02", "", "America/New_York", []string{"2026-11-02T05:00:00Z"}},
		{"only an end", "", "2026-03-07", "America/New_York", []string{"2026-03-08T04:59:59Z"}},
		{"a day and an instant", "2026-03-08", "2026-03-08T05:00:00Z", "America/New_York", []string{"2026-03-08T05:00:00Z"}},
		{"an instant keeps its own offset", "2026-03-08T00:00:00-05:00", "2026-03-08T00:00:00-05:00", "Asia/Tokyo", []string{"2026-03-08T05:00:00Z"}},
		{"the zone alone changes nothing", "2026-11-02T04:30:00Z", "", "Asia/Tokyo", []string{"2026-11-02T04:30:00Z", "2026-11-02T05:00:00Z"}},
		{"the earliest and latest days are not a fault", "0000-01-01", "9999-12-31", "America/New_York", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			query := url.Values{"time_zone": {c.zone}}
			if c.after != "" {
				query.Set("after", c.after)
			}
			if c.before != "" {
				query.Set("before", c.before)
			}
			got := foundInstants(t, searchFor(h, fx, query))
			if c.want != nil && !reflect.DeepEqual(got, c.want) {
				t.Fatalf("found %v, want %v", got, c.want)
			}
		})
	}
}

func searchRefusedFields(t *testing.T, rr *httptest.ResponseRecorder) map[string]string {
	t.Helper()
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body %s", rr.Code, rr.Body.String())
	}
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Details struct {
				Fields map[string]string `json:"fields"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != "validation_failed" || body.Error.Message != "Query parameters failed validation" {
		t.Fatalf("refusal %s", rr.Body.String())
	}
	return body.Error.Details.Fields
}

func TestSearchRefusesWhatItCannotReadInAZone(t *testing.T) {
	h, fx := searchTimeZoneFixture(t)
	const notInZone = "must be an RFC 3339 date-time or a calendar day (YYYY-MM-DD)"
	const notAZone = "must be an IANA time zone name"
	for _, c := range []struct {
		name  string
		query url.Values
		want  map[string]string
	}{
		{"an unknown zone", url.Values{"time_zone": {"Mars/Olympus"}}, map[string]string{"time_zone": notAZone}},
		{"the server's own zone", url.Values{"time_zone": {"Local"}}, map[string]string{"time_zone": notAZone}},
		{"a path out of the zone database", url.Values{"time_zone": {"../../etc/passwd"}}, map[string]string{"time_zone": notAZone}},
		{"a day in an unknown zone blames the zone", url.Values{"time_zone": {"Mars/Olympus"}, "after": {"2026-03-08"}}, map[string]string{"time_zone": notAZone}},
		{"a date no calendar has", url.Values{"time_zone": {"UTC"}, "before": {"2026-02-30"}}, map[string]string{"before": notInZone}},
		{"a date with a stray time", url.Values{"time_zone": {"UTC"}, "after": {"2026-03-08T12:00"}}, map[string]string{"after": notInZone}},
		{"not a date", url.Values{"time_zone": {"UTC"}, "after": {"yesterday"}}, map[string]string{"after": notInZone}},
		{"every field at once", url.Values{"time_zone": {"Mars/Olympus"}, "after": {"yesterday"}, "limit": {"many"}},
			map[string]string{"time_zone": notAZone, "after": notInZone, "limit": "must be an integer"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := searchRefusedFields(t, searchFor(h, fx, c.query)); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("fields %v, want %v", got, c.want)
			}
		})
	}
}

// With no time_zone the route does as it did: after and before are RFC 3339 instants, both
// included, a blank one is omitted, and a calendar day is refused as any other text is.
func TestSearchWithoutAZoneStaysAsItWas(t *testing.T) {
	h, fx := searchTimeZoneFixture(t, "2026-03-08T04:59:59Z", "2026-03-08T05:00:00Z", "2026-03-08T05:00:01Z")
	const notAnInstant = "must be an RFC 3339 date-time"
	for _, c := range []struct {
		name  string
		query url.Values
		want  []string
	}{
		{"neither bound", url.Values{}, []string{"2026-03-08T04:59:59Z", "2026-03-08T05:00:00Z", "2026-03-08T05:00:01Z"}},
		{"both bounds include their instant", url.Values{"after": {"2026-03-08T05:00:00Z"}, "before": {"2026-03-08T05:00:00Z"}}, []string{"2026-03-08T05:00:00Z"}},
		{"an offset names the same instant", url.Values{"after": {"2026-03-08T00:00:01-05:00"}}, []string{"2026-03-08T05:00:01Z"}},
		{"a blank bound is omitted", url.Values{"after": {""}, "before": {"2026-03-08T04:59:59Z"}}, []string{"2026-03-08T04:59:59Z"}},
		{"a blank zone is omitted", url.Values{"time_zone": {""}, "before": {"2026-03-08T04:59:59Z"}}, []string{"2026-03-08T04:59:59Z"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := foundInstants(t, searchFor(h, fx, c.query)); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("found %v, want %v", got, c.want)
			}
		})
	}
	for _, c := range []struct {
		name  string
		query url.Values
		want  map[string]string
	}{
		{"a calendar day", url.Values{"after": {"2026-03-08"}}, map[string]string{"after": notAnInstant}},
		{"a calendar day under a blank zone", url.Values{"time_zone": {""}, "before": {"2026-03-08"}}, map[string]string{"before": notAnInstant}},
		{"other text", url.Values{"before": {"yesterday"}}, map[string]string{"before": notAnInstant}},
		{"an instant with no offset", url.Values{"after": {"2026-03-08T05:00:00"}}, map[string]string{"after": notAnInstant}},
		{"every field at once", url.Values{"after": {"2026-03-08"}, "before": {"x"}, "limit": {"many"}},
			map[string]string{"after": notAnInstant, "before": notAnInstant, "limit": "must be an integer"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := searchRefusedFields(t, searchFor(h, fx, c.query)); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("fields %v, want %v", got, c.want)
			}
		})
	}
}
