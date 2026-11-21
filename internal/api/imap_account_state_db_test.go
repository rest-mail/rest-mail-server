package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	goimap "github.com/rest-mail/go-imap/pkg/imap"
	"github.com/rest-mail/rest-mail-server"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	imapgw "github.com/rest-mail/rest-mail-server/internal/gateway/imap"
	yaml "go.yaml.in/yaml/v2"
)

func TestIMAPAccountDateDSTBoundaries(t *testing.T) {
	for _, test := range []struct {
		name, zone, day, start, transition, end, utcOn string
	}{
		{"north_spring_forward", "America/New_York", "8-Mar-2026", "2026-03-08T05:00:00Z", "2026-03-08T07:00:00Z", "2026-03-09T04:00:00Z", "1 2 3 4 5 6"},
		{"north_fall_back", "America/New_York", "1-Nov-2026", "2026-11-01T04:00:00Z", "2026-11-01T06:00:00Z", "2026-11-02T05:00:00Z", "1 2 3 4 5 6"},
		{"south_fall_back", "Australia/Sydney", "5-Apr-2026", "2026-04-04T13:00:00Z", "2026-04-04T16:00:00Z", "2026-04-05T14:00:00Z", "7 8 9"},
		{"south_spring_forward", "Australia/Sydney", "4-Oct-2026", "2026-10-03T14:00:00Z", "2026-10-03T16:00:00Z", "2026-10-04T13:00:00Z", "7 8 9"},
	} {
		t.Run(test.name, func(t *testing.T) {
			gateway := newGatewayFixture(t)
			fixture := gateway.credentialFixture
			api := gatewayAPI(t, gateway.router, gateway.internal, imapCN)
			session := openIMAPZoneSession(t, fixture, api, gateway.app.Password)
			for _, instant := range []string{test.start, test.transition, test.end} {
				boundary := utcInstant(t, instant)
				for _, offset := range []time.Duration{-time.Second, 0, time.Second} {
					stamp := boundary.Add(offset)
					session.appendAt(stamp, stamp.Format(time.RFC1123Z))
				}
			}
			session.commandOK("SELECT INBOX")
			session.requireSearch("ON "+test.day, test.utcOn)
			setIMAPZoneFixtureZone(t, fixture, fmt.Sprintf(`{"time_zone":%q}`, test.zone), http.StatusOK)
			day, err := time.Parse("2-Jan-2006", test.day)
			if err != nil {
				t.Fatal(err)
			}
			previous := day.AddDate(0, 0, -1).Format("2-Jan-2006")
			next := day.AddDate(0, 0, 1).Format("2-Jan-2006")
			session.requireSearch("ON "+previous, "1")
			session.requireSearch("ON "+test.day, "2 3 4 5 6 7")
			session.requireSearch("ON "+next, "8 9")
			session.requireSearch("BEFORE "+test.day, "1")
			session.requireSearch("SINCE "+next, "8 9")
			setIMAPZoneFixtureZone(t, fixture, `{"time_zone":null}`, http.StatusOK)
			session.requireSearch("ON "+test.day, test.utcOn)
		})
	}
}

func TestIMAPAccountDateOriginalOffsetReproducer(t *testing.T) {
	gateway := newGatewayFixture(t)
	fixture := gateway.credentialFixture
	api := gatewayAPI(t, gateway.router, gateway.internal, imapCN)
	session := openIMAPZoneSession(t, fixture, api, gateway.app.Password)
	instant := utcInstant(t, "1994-02-08T06:43:04Z")
	session.appendAt(instant.In(time.FixedZone("PST", -8*60*60)), "Mon, 7 Feb 1994 22:43:04 -0800")
	session.commandOK("SELECT INBOX")

	assertTimestamp := func() {
		t.Helper()
		var message models.Message
		if err := fixture.db.Where("mailbox_id = ?", fixture.mailboxID).Take(&message).Error; err != nil {
			t.Fatal(err)
		}
		if !message.ReceivedAt.Equal(instant) {
			t.Errorf("stored received_at = %v, want %v", message.ReceivedAt, instant)
		}
		response := fixture.do(http.MethodGet, fmt.Sprintf("/api/v1/messages/%d", message.ID), fixture.session, "")
		var body struct {
			Data struct {
				ReceivedAt string              `json:"received_at"`
				DateHeader string              `json:"date_header"`
				Headers    map[string][]string `json:"headers"`
			} `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || response.Code != http.StatusOK {
			t.Fatalf("REST message: %d %s (%v)", response.Code, response.Body, err)
		}
		if body.Data.ReceivedAt != "1994-02-08T06:43:04Z" || body.Data.DateHeader != "1994-02-08T06:43:04Z" {
			t.Errorf("REST received_at/date_header = %q/%q, want 1994-02-08T06:43:04Z for both", body.Data.ReceivedAt, body.Data.DateHeader)
		}
		if rawDate := body.Data.Headers["Date"]; !slices.Equal(rawDate, []string{"Mon, 7 Feb 1994 22:43:04 -0800"}) {
			t.Errorf("raw Date header was changed: %q", rawDate)
		}
		lines := session.commandOK("FETCH 1 INTERNALDATE")
		if len(lines) != 1 || !strings.Contains(lines[0], `INTERNALDATE "08-Feb-1994 06:43:04 +0000"`) {
			t.Errorf("FETCH INTERNALDATE = %q, want 08-Feb-1994 06:43:04 +0000", lines)
		}
	}
	assertTimestamp()
	setIMAPZoneFixtureZone(t, fixture, `{"time_zone":"America/Los_Angeles"}`, http.StatusOK)
	session.requireSearch("ON 7-Feb-1994", "1")
	session.requireSearch("ON 8-Feb-1994", "")
	setIMAPZoneFixtureZone(t, fixture, `{"time_zone":"Local"}`, http.StatusUnprocessableEntity)
	session.requireSearch("ON 7-Feb-1994", "1")
	assertTimestamp()
	setIMAPZoneFixtureZone(t, fixture, `{"time_zone":null}`, http.StatusOK)
	session.requireSearch("ON 7-Feb-1994", "")
	session.requireSearch("ON 8-Feb-1994", "1")
	assertTimestamp()
}

func TestIMAPAccountDateDescription(t *testing.T) {
	const path = "/api/v1/accounts/{id}/time-zone"
	_, served := liveDescription(t, describedRouters(t).Public)
	published, err := restmail.OpenAPI.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	type operation struct {
		Description string `json:"description" yaml:"description"`
	}
	var liveDoc, savedDoc struct {
		Paths map[string]struct {
			Get operation `json:"get" yaml:"get"`
			Put operation `json:"put" yaml:"put"`
		} `json:"paths" yaml:"paths"`
	}
	if err := json.Unmarshal(served, &liveDoc); err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(published, &savedDoc); err != nil {
		t.Fatal(err)
	}
	const messagePath = "/api/v1/messages/{id}"
	const folderPath = "/api/v1/accounts/{id}/folders/{folder}/messages"
	messageClauses := []string{"received_at, date_header, created_at and updated_at timestamps use UTC", "regardless of the account time zone", "The raw Date header is unchanged"}
	for _, test := range []struct {
		name, live, saved string
		clauses           []string
	}{
		{"getAccountTimeZone", liveDoc.Paths[path].Get.Description, savedDoc.Paths[path].Get.Description, []string{"calendar days IMAP SEARCH ON, BEFORE and SINCE", "Null when none is set, and IMAP then uses UTC", "Timestamps are stored and returned in UTC"}},
		{"setAccountTimeZone", liveDoc.Paths[path].Put.Description, savedDoc.Paths[path].Put.Description, []string{"calendar days IMAP SEARCH ON, BEFORE and SINCE", "null to clear it and use UTC", "including on connections already open", "An unknown name is refused", "Stored timestamps do not change", "IMAP INTERNALDATE is still rendered in UTC"}},
		{"getMessage", liveDoc.Paths[messagePath].Get.Description, savedDoc.Paths[messagePath].Get.Description, messageClauses},
		{"listMessages", liveDoc.Paths[folderPath].Get.Description, savedDoc.Paths[folderPath].Get.Description, messageClauses},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.live != test.saved {
				t.Errorf("live description %q disagrees with openapi.yaml %q", test.live, test.saved)
			}
			for _, clause := range test.clauses {
				if !strings.Contains(test.live, clause) {
					t.Errorf("description %q lacks observed behavior %q", test.live, clause)
				}
			}
		})
	}
}

func (session *imapZoneSession) commandOK(command string) []string {
	session.t.Helper()
	lines, status := session.command(command)
	if !strings.Contains(status, " OK") {
		session.t.Fatalf("%s: %q / %q", command, lines, status)
	}
	return lines
}

func requireIMAPLines(t *testing.T, lines []string, expected ...string) {
	t.Helper()
	for _, want := range expected {
		if !slices.Contains(lines, want) {
			t.Errorf("wire responses %q do not include %q", lines, want)
		}
	}
}

func requireQResyncDelta(t *testing.T, lines []string, expected ...string) {
	t.Helper()
	var delta []string
	for _, line := range lines {
		if strings.HasPrefix(line, "* VANISHED") || strings.Contains(line, " FETCH (") || strings.HasSuffix(line, " EXPUNGE") {
			delta = append(delta, line)
		}
	}
	if !slices.Equal(delta, expected) {
		t.Errorf("QRESYNC delta = %q, want %q (all responses: %q)", delta, expected, lines)
	}
}

type qresyncEpochBackend struct {
	goimap.Backend
	validity uint32
}

func (backend qresyncEpochBackend) Authenticate(user, pass string) (goimap.Mailbox, error) {
	mailbox, err := backend.Backend.Authenticate(user, pass)
	if err != nil {
		return nil, err
	}
	return qresyncEpochMailbox{
		MailboxCloser: mailbox.(goimap.MailboxCloser), UIDPlusMailbox: mailbox.(goimap.UIDPlusMailbox),
		UIDNextMailbox: mailbox.(goimap.UIDNextMailbox), ModSeqMailbox: mailbox.(goimap.ModSeqMailbox),
		VanishedMailbox: mailbox.(goimap.VanishedMailbox), RecentMailbox: mailbox.(goimap.RecentMailbox), validity: backend.validity,
	}, nil
}

type qresyncEpochMailbox struct {
	goimap.MailboxCloser
	goimap.UIDPlusMailbox
	goimap.UIDNextMailbox
	goimap.ModSeqMailbox
	goimap.VanishedMailbox
	goimap.RecentMailbox
	validity uint32
}

func (mailbox qresyncEpochMailbox) UIDValidity(string) (uint32, error) {
	return mailbox.validity, nil
}

func TestIMAPQResyncRetainsDepartures(t *testing.T) {
	gateway := newGatewayFixture(t)
	fixture := gateway.credentialFixture
	api := gatewayAPI(t, gateway.router, gateway.internal, imapCN)
	backend := imapgw.NewBackend(api, nil)
	prepareIMAPImport(t, fixture)
	open := func(t *testing.T, backend goimap.Backend) *imapZoneSession {
		t.Helper()
		session := openIMAPWireSession(t, backend, fixture.address, gateway.app.Password)
		requireIMAPLines(t, session.commandOK("ENABLE QRESYNC"), "* ENABLED QRESYNC")
		return session
	}
	first := open(t, backend)
	stamp := utcInstant(t, "2020-01-01T00:00:00Z")
	for index := range 5 {
		instant := stamp.Add(time.Duration(index) * time.Second)
		first.appendAt(instant, instant.Format(time.RFC1123Z))
	}
	first.commandOK("CREATE ResyncArchive")
	before := first.commandOK("SELECT INBOX")
	requireIMAPLines(t, before, "* 5 EXISTS", "* OK [UIDVALIDITY 1] UIDs valid", "* OK [HIGHESTMODSEQ 6] Highest")
	requireIMAPLines(t, first.commandOK("UID SEARCH ALL"), "* SEARCH 1 2 3 4 5")
	first.commandOK("LOGOUT")

	writer := open(t, backend)
	writer.commandOK("SELECT INBOX")
	writer.commandOK(`UID STORE 2 +FLAGS.SILENT (\Seen)`)
	writer.commandOK(`UID STORE 3 +FLAGS.SILENT (\Deleted)`)
	requireIMAPLines(t, writer.commandOK("UID EXPUNGE 3"), "* VANISHED 3")
	requireIMAPLines(t, writer.commandOK("UID MOVE 4 ResyncArchive"), "* VANISHED 4")
	response := fixture.do(http.MethodDelete, "/api/v1/messages/5?permanent=true", fixture.session, "")
	if response.Code != http.StatusNoContent {
		t.Fatalf("delete UID 5: %d %s", response.Code, response.Body)
	}
	writer.commandOK("LOGOUT")

	const advanced = 1_000_000
	updated := fixture.db.Model(&models.FolderModSeq{}).Where("mailbox_id = ? AND folder = ? AND highest_mod_seq = ?", fixture.mailboxID, "INBOX", 11).Update("highest_mod_seq", advanced)
	if updated.Error != nil || updated.RowsAffected != 1 {
		t.Fatalf("advance INBOX from mod-sequence 11: rows=%d err=%v", updated.RowsAffected, updated.Error)
	}
	writer = open(t, backend)
	newStamp := utcInstant(t, "2026-11-01T06:00:00Z")
	var tag string
	var newUID uint32
	status := writer.appendAt(newStamp, newStamp.Format(time.RFC1123Z))
	if _, err := fmt.Sscanf(status, "%s OK [APPENDUID 1 %d]", &tag, &newUID); err != nil || newUID <= 5 {
		t.Fatalf("APPEND after reconnect cursor did not allocate a new UID: %q (%v)", status, err)
	}
	writer.commandOK("SELECT INBOX")
	writer.commandOK("LOGOUT")

	t.Run("retained_in_postgresql", func(t *testing.T) {
		var rows []models.FolderVanishedUID
		if err := fixture.db.Where("mailbox_id = ? AND folder = ?", fixture.mailboxID, "INBOX").Order("uid").Find(&rows).Error; err != nil {
			t.Fatal(err)
		}
		var departures []string
		for _, row := range rows {
			departures = append(departures, fmt.Sprintf("%d@%d", row.UID, row.ModSeq))
		}
		if !slices.Equal(departures, []string{"3@9", "4@10", "5@11"}) {
			t.Errorf("retained departures = %v, want [3@9 4@10 5@11]", departures)
		}
	})
	const changed = `* 2 FETCH (UID 2 FLAGS (\Seen) MODSEQ (7))`
	t.Run("known_uids", func(t *testing.T) {
		session := open(t, backend)
		lines := session.commandOK("SELECT INBOX (QRESYNC (1 6 1:5))")
		requireIMAPLines(t, lines, "* 3 EXISTS", "* OK [HIGHESTMODSEQ 1000001] Highest")
		requireQResyncDelta(t, lines, "* VANISHED (EARLIER) 3:5", changed)
		requireQResyncDelta(t, session.commandOK("UID FETCH 1:3 (FLAGS) (CHANGEDSINCE 6 VANISHED)"), "* VANISHED (EARLIER) 3", changed)
	})
	t.Run("no_known_set", func(t *testing.T) {
		session := open(t, backend)
		lines := session.commandOK("SELECT INBOX (QRESYNC (1 6))")
		requireQResyncDelta(t, lines, "* VANISHED (EARLIER) 3:5", changed, fmt.Sprintf(`* 3 FETCH (UID %d FLAGS () MODSEQ (1000001))`, newUID))
		requireQResyncDelta(t, session.commandOK("SELECT INBOX (QRESYNC (1 1000001))"))
	})
	t.Run("uidvalidity_changed", func(t *testing.T) {
		session := open(t, qresyncEpochBackend{Backend: backend.ForClient(testClientIP), validity: 2})
		lines := session.commandOK("SELECT INBOX (QRESYNC (1 6 1:5))")
		requireIMAPLines(t, lines, "* 3 EXISTS", "* OK [UIDVALIDITY 2] UIDs valid")
		requireQResyncDelta(t, lines)
		requireIMAPLines(t, session.commandOK("UID SEARCH ALL"), fmt.Sprintf("* SEARCH 1 2 %d", newUID))
		lines = session.commandOK("SELECT INBOX (QRESYNC (2 6 1:5))")
		requireQResyncDelta(t, lines, "* VANISHED (EARLIER) 3:5", changed)
	})
}
