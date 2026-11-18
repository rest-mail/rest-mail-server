package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	rmdb "github.com/rest-mail/rest-mail-server/internal/db"
)

// A create answers with the timestamps every later read answers with (#2044), on the real
// router and a real database. The database clock here is finer than the column on every
// platform, as Linux's is and macOS's is not. A scheduled send also stores the instant its
// client chose, which can be finer than the column anywhere.
func TestCreateAnswersWithTheTimestampsALaterReadReturns(t *testing.T) {
	f := newGrantsFixture(t)
	f.db.NowFunc = rmdb.StoredClock(func() time.Time {
		return time.Now().Truncate(time.Microsecond).Add(789 * time.Nanosecond)
	})
	token := f.token(t, "owner")
	scheduledAt := time.Now().Add(time.Hour).UTC().Truncate(time.Second).Add(123456789 * time.Nanosecond)
	for _, tc := range []struct {
		name, create, body string
		status             int
		read               string
		fields             []string
	}{
		{"alias", "/api/v1/admin/aliases", `{"source_address":"stamped@a.test","destination_address":"one@elsewhere.test"}`,
			http.StatusCreated, "/api/v1/admin/aliases/%d", []string{"created_at"}},
		{"draft", "/api/v1/messages/draft", `{"from":"owner@access.test","to":["owner@access.test"],"subject":"Stamped"}`,
			http.StatusCreated, "/api/v1/messages/%d", []string{"created_at"}},
		{"scheduled send", "/api/v1/messages/send", fmt.Sprintf(`{"from":"owner@access.test","to":["owner@access.test"],"subject":"Stamped","body_text":"Later.","scheduled_at":%q}`,
			scheduledAt.Format(time.RFC3339Nano)), http.StatusAccepted, "/api/v1/messages/scheduled", []string{"scheduled_at", "created_at", "updated_at"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			created := doRequest(f.router, http.MethodPost, tc.create, token, tc.body)
			if created.Code != tc.status {
				t.Fatalf("create: %d %s", created.Code, created.Body)
			}
			answered := answeredRow(t, created.Body.Bytes(), 0)
			id := answered["id"].(float64)
			path := tc.read
			if tc.status == http.StatusCreated {
				path = fmt.Sprintf(tc.read, int64(id))
			}
			read := doRequest(f.router, http.MethodGet, path, token, "")
			if read.Code != http.StatusOK {
				t.Fatalf("read: %d %s", read.Code, read.Body)
			}
			stored := answeredRow(t, read.Body.Bytes(), id)
			for _, field := range tc.fields {
				if !answeredInstant(t, answered, field).Equal(answeredInstant(t, stored, field)) {
					t.Errorf("%s: create answered %v, read answered %v", field, answered[field], stored[field])
				}
			}
		})
	}
}

// answeredInstant is a timestamp field of a row. It is compared as an instant: a create and a
// read can write the same one with different UTC offsets.
func answeredInstant(t *testing.T, row map[string]any, field string) time.Time {
	t.Helper()
	text, _ := row[field].(string)
	at, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		t.Fatalf("%s: %v", field, err)
	}
	return at
}

// answeredRow is a response's data, or the row of its data list that has the id.
func answeredRow(t *testing.T, body []byte, id float64) map[string]any {
	t.Helper()
	var response struct {
		Data any `json:"data"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatalf("not JSON: %s", body)
	}
	if row, ok := response.Data.(map[string]any); ok {
		return row
	}
	rows, _ := response.Data.([]any)
	for _, item := range rows {
		if row, ok := item.(map[string]any); ok && row["id"] == id {
			return row
		}
	}
	t.Fatalf("no row %v in %s", id, body)
	return nil
}
