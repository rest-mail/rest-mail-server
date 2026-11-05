package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/api"
	apitypes "github.com/rest-mail/rest-mail-server/internal/api/types"
	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/db/repositories"
	"github.com/rest-mail/rest-mail-server/internal/grants"
	"gorm.io/gorm"
)

// mailqueue_test.go checks what the command sends, against a stand-in. This
// file checks that what it sends is what the server takes: the real public
// router on PostgreSQL, with real API keys, so a wrong method, path, body shape
// or capability fails here. The rows it acts on are read back from the database.

// queueServer is the real API and the keys an operator would hold.
type queueServer struct {
	db     *gorm.DB
	url    string
	client *http.Client
	manage string // queue:read and queue:manage
	read   string // queue:read only
}

func newQueueServer(t *testing.T) *queueServer {
	t.Helper()
	db := migrationDatabase(t)
	jwt := auth.NewJWTService("mail-queue-test-signing-key-of-no-value", 5*time.Minute, time.Hour)
	routers := api.NewRouters(db, jwt, &config.Config{Environment: "test", SMTPMaxMessageSize: 1 << 20, APIMaxBodyBytes: 4 << 20}, nil)
	t.Cleanup(routers.Close)
	server := httptest.NewServer(routers.Public)
	t.Cleanup(server.Close)

	domain := models.Domain{Name: migrationDomain, Active: true, DefaultQuotaBytes: 1 << 30}
	if err := db.Create(&domain).Error; err != nil {
		t.Fatalf("create domain: %v", err)
	}
	holder := models.Mailbox{DomainID: domain.ID, LocalPart: "operator", Address: "operator@" + migrationDomain, Active: true}
	if err := db.Create(&holder).Error; err != nil {
		t.Fatalf("create the operator: %v", err)
	}
	var admin models.Role
	if err := db.Where("name = ?", "admin").First(&admin).Error; err != nil {
		t.Fatalf("find the admin role: %v", err)
	}
	if err := db.Create(&models.Grant{HolderKind: models.GrantHolderAccount, HolderID: holder.ID, RoleID: admin.ID,
		Scope: grants.All, Mode: models.GrantActive, GrantedByKind: models.AuditActorSystem, GrantedAt: time.Now()}).Error; err != nil {
		t.Fatalf("grant the admin role: %v", err)
	}
	key := func(name string, capabilities ...string) string {
		permissions := make([]auth.ScopedPermission, 0, len(capabilities))
		for _, capability := range capabilities {
			permissions = append(permissions, auth.ScopedPermission{Capability: capability, Scope: grants.All})
		}
		_, secret, err := repositories.NewCredentialRepository(db).CreateAPIKey(holder.ID, holder.ID, name, permissions, nil)
		if err != nil {
			t.Fatalf("create the %s key: %v", name, err)
		}
		return secret
	}
	return &queueServer{db: db, url: server.URL, client: server.Client(),
		manage: key("manage", "queue:read", "queue:manage"), read: key("read", "queue:read")}
}

// queueRows names the entries seed makes.
type queueRows struct {
	pending, greylisted, refused, delivering, delivered, bounced, expired uint
}

// seed replaces the queue with one known entry of each kind, so that no check
// depends on what an earlier one did.
func (s *queueServer) seed(t *testing.T) queueRows {
	t.Helper()
	if err := s.db.Where("1 = 1").Delete(&models.OutboundQueue{}).Error; err != nil {
		t.Fatalf("empty the queue: %v", err)
	}
	later := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	entry := func(sender, recipient, status string, attempts int, lastError string, code int, category models.FailureCategory) uint {
		row := models.OutboundQueue{
			Sender: sender, Recipient: recipient, Domain: recipient[strings.IndexByte(recipient, '@')+1:],
			RawMessage: "Subject: queued\r\n\r\nbody\r\n", Status: status, Attempts: attempts, NextAttempt: later,
			LastError: lastError, LastErrorCode: code, FailureCategory: category, ExpiresAt: later.Add(72 * time.Hour),
		}
		if err := s.db.Create(&row).Error; err != nil {
			t.Fatalf("create the %s entry: %v", status, err)
		}
		return row.ID
	}
	return queueRows{
		pending:    entry("alice@example.test", "bob@example.org", "pending", 0, "", 0, ""),
		greylisted: entry("alice@example.test", "carol@example.org", "deferred", 3, "451 4.7.1 greylisted\r\nplease try later", 451, models.FailureGreylisted),
		refused:    entry("dave@example.test", "erin@example.net", "deferred", 7, "connection refused", 0, models.FailureConnection),
		delivering: entry("alice@example.test", "frank@example.net", "delivering", 1, "", 0, ""),
		delivered:  entry("alice@example.test", "grace@example.org", "delivered", 1, "", 0, ""),
		bounced:    entry("dave@example.test", "heidi@example.org", "bounced", 1, "550 5.1.1 no such user", 550, models.FailureBadMailbox),
		expired:    entry("dave@example.test", "judy@example.com", "expired", 60, "timed out", 0, models.FailureConnection),
	}
}

func (s *queueServer) run(t *testing.T, key, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	full := append([]string{args[0], "--server", s.url}, args[1:]...)
	code := runMailQueue(context.Background(), full, key, strings.NewReader(stdin), s.client, &out, &errOut)
	return code, out.String(), errOut.String()
}

// statuses is each entry's status in the database, by ID.
func (s *queueServer) statuses(t *testing.T) map[uint]string {
	t.Helper()
	var rows []models.OutboundQueue
	if err := s.db.Find(&rows).Error; err != nil {
		t.Fatalf("read the queue: %v", err)
	}
	found := make(map[uint]string, len(rows))
	for _, row := range rows {
		found[row.ID] = row.Status
	}
	return found
}

func (s *queueServer) wantStatuses(t *testing.T, want map[uint]string) {
	t.Helper()
	got := s.statuses(t)
	if len(got) != len(want) {
		t.Errorf("queue holds %d entries, want %d: %v", len(got), len(want), got)
	}
	for id, status := range want {
		if got[id] != status {
			t.Errorf("entry %d is %q, want %q", id, got[id], status)
		}
	}
}

func (r queueRows) statuses() map[uint]string {
	return map[uint]string{r.pending: "pending", r.greylisted: "deferred", r.refused: "deferred",
		r.delivering: "delivering", r.delivered: "delivered", r.bounced: "bounced", r.expired: "expired"}
}

// with is the seeded statuses after an action changed or, with "", removed some.
func (r queueRows) with(changes map[uint]string) map[uint]string {
	want := r.statuses()
	for id, status := range changes {
		if status == "" {
			delete(want, id)
			continue
		}
		want[id] = status
	}
	return want
}

func TestMailQueueOnTheRealAPI(t *testing.T) {
	s := newQueueServer(t)

	t.Run("stats counts each status", func(t *testing.T) {
		s.seed(t)
		code, out, errOut := s.run(t, s.read, "", "stats", "--json")
		if code != exitOK {
			t.Fatalf("exit %d: %s", code, errOut)
		}
		var stats apitypes.QueueStatistics
		if err := json.Unmarshal([]byte(out), &stats); err != nil {
			t.Fatalf("stats: %v: %s", err, out)
		}
		want := apitypes.QueueStatistics{Pending: 1, Delivering: 1, Deferred: 2, Delivered: 1, Bounced: 1, Expired: 1, Total: 7}
		if stats != want {
			t.Errorf("stats = %+v, want %+v", stats, want)
		}
		if code, out, _ := s.run(t, s.read, "", "stats"); code != exitOK || !strings.Contains(strings.Join(strings.Fields(out), " "), "deferred 2") {
			t.Errorf("stats as text: exit %d: %s", code, out)
		}
	})

	t.Run("list filters on the server", func(t *testing.T) {
		r := s.seed(t)
		for name, tc := range map[string]struct {
			args []string
			want []uint
		}{
			"status":    {[]string{"--status", "deferred"}, []uint{r.greylisted, r.refused}},
			"domain":    {[]string{"--domain", "example.net"}, []uint{r.refused, r.delivering}},
			"sender":    {[]string{"--sender", "DAVE"}, []uint{r.refused, r.bounced, r.expired}},
			"recipient": {[]string{"--recipient", "heidi"}, []uint{r.bounced}},
			"together":  {[]string{"--status", "deferred", "--domain", "example.org", "--sender", "alice"}, []uint{r.greylisted}},
		} {
			args := append([]string{"list", "--json"}, tc.args...)
			code, out, errOut := s.run(t, s.read, "", args...)
			if code != exitOK {
				t.Errorf("%s: exit %d: %s", name, code, errOut)
				continue
			}
			var page queuePage
			if err := json.Unmarshal([]byte(out), &page); err != nil {
				t.Errorf("%s: %v: %s", name, err, out)
				continue
			}
			got := make([]uint, 0, len(page.Items))
			for _, item := range page.Items {
				got = append(got, item.ID)
			}
			slices.Sort(got)
			slices.Sort(tc.want)
			if !slices.Equal(got, tc.want) || page.Total != int64(len(tc.want)) {
				t.Errorf("%s: entries %v of %d, want %v", name, got, page.Total, tc.want)
			}
		}
	})

	t.Run("list says why an entry is waiting", func(t *testing.T) {
		r := s.seed(t)
		code, out, errOut := s.run(t, s.read, "", "list", "--status", "deferred", "--recipient", "carol")
		if code != exitOK {
			t.Fatalf("exit %d: %s", code, errOut)
		}
		var entry models.OutboundQueue
		if err := s.db.First(&entry, r.greylisted).Error; err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"greylisted", "451 4.7.1 greylisted please try later", entry.NextAttempt.UTC().Format(time.RFC3339), "1–1 of 1"} {
			if !strings.Contains(out, want) {
				t.Errorf("list has no %q:\n%s", want, out)
			}
		}
		fields := strings.Fields(strings.Split(out, "\n")[1])
		if fields[0] != strconv.FormatUint(uint64(r.greylisted), 10) || fields[2] != "3" {
			t.Errorf("row is not entry %d with 3 attempts: %v", r.greylisted, fields)
		}
	})

	t.Run("show one entry", func(t *testing.T) {
		r := s.seed(t)
		code, out, errOut := s.run(t, s.read, "", "show", strconv.FormatUint(uint64(r.bounced), 10))
		if code != exitOK {
			t.Fatalf("exit %d: %s", code, errOut)
		}
		for _, want := range []string{"heidi@example.org", "bad_mailbox", "550 5.1.1 no such user", "1 of "} {
			if !strings.Contains(out, want) {
				t.Errorf("show has no %q:\n%s", want, out)
			}
		}
		if code, _, errOut := s.run(t, s.read, "", "show", "999999"); code != exitFailed || !strings.Contains(errOut, "Queue entry not found") {
			t.Errorf("a missing entry: exit %d: %s", code, errOut)
		}
	})

	t.Run("act on one entry", func(t *testing.T) {
		for _, tc := range []struct {
			name, sub, says string
			target          func(queueRows) uint
			after           string
		}{
			{"retry a deferred entry", "retry", "retried entry", func(r queueRows) uint { return r.refused }, "pending"},
			{"bounce a pending entry", "bounce", "bounced entry", func(r queueRows) uint { return r.pending }, "bounced"},
			{"bounce one in flight", "bounce", "bounced entry", func(r queueRows) uint { return r.delivering }, "bounced"},
			{"purge any entry", "purge", "purged entry", func(r queueRows) uint { return r.delivered }, ""},
		} {
			r := s.seed(t)
			id := tc.target(r)
			code, out, errOut := s.run(t, s.manage, "", tc.sub, strconv.FormatUint(uint64(id), 10))
			if code != exitOK || !strings.Contains(out, tc.says) {
				t.Errorf("%s: exit %d: %s%s", tc.name, code, out, errOut)
			}
			s.wantStatuses(t, r.with(map[uint]string{id: tc.after}))
		}
	})

	t.Run("a retry is due now", func(t *testing.T) {
		r := s.seed(t)
		if code, _, errOut := s.run(t, s.manage, "", "retry", strconv.FormatUint(uint64(r.greylisted), 10)); code != exitOK {
			t.Fatalf("exit %d: %s", code, errOut)
		}
		var entry models.OutboundQueue
		if err := s.db.First(&entry, r.greylisted).Error; err != nil {
			t.Fatal(err)
		}
		if entry.NextAttempt.After(time.Now()) {
			t.Errorf("next attempt is %s, in the future", entry.NextAttempt)
		}
	})

	t.Run("the server's refusal of one entry is shown and changes nothing", func(t *testing.T) {
		r := s.seed(t)
		for _, tc := range []struct {
			sub  string
			id   uint
			says string
		}{
			{"retry", r.delivered, "Only pending or deferred entries can be retried"},
			{"bounce", r.expired, "Only pending, deferred or delivering entries can be bounced"},
			{"purge", 999999, "Queue entry not found"},
		} {
			code, _, errOut := s.run(t, s.manage, "", tc.sub, strconv.FormatUint(uint64(tc.id), 10))
			if code != exitFailed || !strings.Contains(errOut, tc.says) {
				t.Errorf("%s %d: exit %d: %s", tc.sub, tc.id, code, errOut)
			}
		}
		s.wantStatuses(t, r.statuses())
	})

	t.Run("a filter says how many, asks, and touches exactly those", func(t *testing.T) {
		for _, tc := range []struct {
			name     string
			args     []string
			question string
			done     string
			after    func(queueRows) map[uint]string
		}{
			{"retry by status", []string{"retry", "--status", "deferred"}, "Retry 2 deferred entries? [y/N]", "retried 2 entries",
				func(r queueRows) map[uint]string {
					return map[uint]string{r.greylisted: "pending", r.refused: "pending"}
				}},
			{"retry by domain passes over other statuses", []string{"retry", "--domain", "example.net"}, "Retry 1 pending, deferred entry? [y/N]", "retried 1 entry",
				func(r queueRows) map[uint]string { return map[uint]string{r.refused: "pending"} }},
			{"bounce by sender", []string{"bounce", "--sender", "alice"}, "Bounce 3 pending, deferred, delivering entries? [y/N]", "bounced 3 entries",
				func(r queueRows) map[uint]string {
					return map[uint]string{r.pending: "bounced", r.greylisted: "bounced", r.delivering: "bounced"}
				}},
			{"purge by status", []string{"purge", "--status", "bounced"}, "Purge 1 bounced entry? [y/N]", "purged 1 entry",
				func(r queueRows) map[uint]string { return map[uint]string{r.bounced: ""} }},
			{"purge by recipient", []string{"purge", "--recipient", "example.org", "--sender", "dave"}, "Purge 1 pending, delivering, deferred, delivered, bounced, expired entry? [y/N]", "purged 1 entry",
				func(r queueRows) map[uint]string { return map[uint]string{r.bounced: ""} }},
		} {
			r := s.seed(t)
			for _, answer := range []string{"n\n", "maybe\n", ""} {
				code, _, errOut := s.run(t, s.manage, answer, tc.args...)
				if code != exitFailed || !strings.Contains(errOut, tc.question) || !strings.Contains(errOut, "nothing changed") {
					t.Errorf("%s, answering %q: exit %d: %s", tc.name, answer, code, errOut)
				}
				s.wantStatuses(t, r.statuses())
			}
			code, out, errOut := s.run(t, s.manage, "y\n", tc.args...)
			if code != exitOK || !strings.Contains(errOut, tc.question) || !strings.Contains(out, tc.done) {
				t.Errorf("%s, answering y: exit %d: %s%s", tc.name, code, out, errOut)
			}
			s.wantStatuses(t, r.with(tc.after(r)))

			r = s.seed(t)
			code, out, errOut = s.run(t, s.manage, "", append(tc.args, "--yes")...)
			if code != exitOK || strings.Contains(errOut, "[y/N]") || !strings.Contains(out, tc.done) {
				t.Errorf("%s, with --yes: exit %d: %s%s", tc.name, code, out, errOut)
			}
			s.wantStatuses(t, r.with(tc.after(r)))
		}
	})

	t.Run("--all selects every entry the action applies to", func(t *testing.T) {
		r := s.seed(t)
		code, out, errOut := s.run(t, s.manage, "", "bounce", "--all", "--yes", "--json")
		if code != exitOK {
			t.Fatalf("bounce: exit %d: %s", code, errOut)
		}
		var outcome struct{ Matched, Affected int }
		if err := json.Unmarshal([]byte(out), &outcome); err != nil || outcome.Matched != 4 || outcome.Affected != 4 {
			t.Errorf("bounce --all: %s (%v)", out, err)
		}
		s.wantStatuses(t, r.with(map[uint]string{r.pending: "bounced", r.greylisted: "bounced", r.refused: "bounced", r.delivering: "bounced"}))

		code, out, errOut = s.run(t, s.manage, "y\n", "purge", "--all")
		if code != exitOK || !strings.Contains(errOut, "Purge 7 ") || !strings.Contains(out, "purged 7 entries") {
			t.Errorf("purge --all: exit %d: %s%s", code, out, errOut)
		}
		s.wantStatuses(t, map[uint]string{})
	})

	t.Run("a filter nothing matches sends nothing and asks nothing", func(t *testing.T) {
		r := s.seed(t)
		code, out, errOut := s.run(t, s.manage, "", "retry", "--status", "delivered")
		if code != exitOK || strings.Contains(errOut, "[y/N]") || !strings.Contains(out, "nothing to retry") {
			t.Errorf("exit %d: %s%s", code, out, errOut)
		}
		s.wantStatuses(t, r.statuses())
	})

	t.Run("a key with queue:read cannot change the queue", func(t *testing.T) {
		r := s.seed(t)
		for _, args := range [][]string{
			{"retry", strconv.FormatUint(uint64(r.refused), 10)},
			{"bounce", strconv.FormatUint(uint64(r.pending), 10)},
			{"purge", strconv.FormatUint(uint64(r.bounced), 10)},
			{"retry", "--status", "deferred", "--yes"},
			{"bounce", "--status", "pending", "--yes"},
			{"purge", "--status", "bounced", "--yes"},
		} {
			code, _, errOut := s.run(t, s.read, "", args...)
			if code != exitFailed || !strings.Contains(errOut, "Insufficient permissions") {
				t.Errorf("%v: exit %d: %s", args, code, errOut)
			}
		}
		s.wantStatuses(t, r.statuses())
	})

	t.Run("a key the server does not know is refused", func(t *testing.T) {
		code, _, errOut := s.run(t, "not-a-key", "", "stats")
		if code != exitFailed || !strings.Contains(errOut, "Invalid or expired token") {
			t.Errorf("exit %d: %s", code, errOut)
		}
	})
}
