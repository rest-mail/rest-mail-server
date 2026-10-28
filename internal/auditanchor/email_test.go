package auditanchor

import (
	"io"
	"net/mail"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/audit"
	"github.com/restmail/restmail/internal/db/models"
)

var testNow = time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)

func previousAnchor(at time.Time) models.AuditEntry {
	return models.AuditEntry{Seq: 11, OccurredAt: at, Action: emailAction,
		Target: models.AuditTarget{"seq": "10", "hash": strings.Repeat("a", 64)}}
}

func TestEmailAnchorContentAndAuditEntry(t *testing.T) {
	head := audit.Anchor{Seq: 42, Hash: strings.Repeat("b", 64)}
	previous := previousAnchor(testNow.Add(-24 * time.Hour))
	p, err := plan("mx.example.test", "audit-anchor@example.test", []string{"owner@outside.test"}, testNow, head, previous, changeWindow{})
	if err != nil || p == nil {
		t.Fatalf("plan: %v, %v", p, err)
	}
	if len(p.messages) != 1 {
		t.Fatalf("messages: %d", len(p.messages))
	}
	q := p.messages[0]
	m, err := mail.ReadMessage(strings.NewReader(q.RawMessage))
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(m.Body)
	if err != nil {
		t.Fatal(err)
	}
	want := "REST Mail audit anchor\r\nServer: mx.example.test\r\nTime: 2026-10-28T09:00:00Z\r\nSeq: 42\r\nHash: " + head.Hash + "\r\nPrevious-Seq: 10\r\nPrevious-Hash: " + strings.Repeat("a", 64) + "\r\n"
	if string(body) != want {
		t.Fatalf("body = %q; want %q", body, want)
	}
	if len(body) >= 1024 {
		t.Fatalf("anchor is %d bytes", len(body))
	}
	if m.Header.Get("Content-Type") != "text/plain; charset=utf-8" || m.Header.Get("To") != q.Recipient {
		t.Fatalf("headers: %v", m.Header)
	}
	if q.Sender != "audit-anchor@example.test" || q.Recipient != "owner@outside.test" || q.Domain != "outside.test" || q.Status != "pending" || !q.NextAttempt.Equal(testNow) {
		t.Fatalf("queue: %+v", q)
	}
	e := p.entry
	if e.Action != emailAction || e.ActorKind != models.AuditActorSystem || e.Outcome != models.AuditOutcomeSuccess || !e.OccurredAt.Equal(testNow) {
		t.Fatalf("audit entry: %+v", e)
	}
	for k, v := range map[string]string{"seq": "42", "hash": head.Hash, "previous_seq": "10", "previous_hash": strings.Repeat("a", 64), "delivery": "queued"} {
		if e.Target[k] != v {
			t.Errorf("audit %s = %q; want %q", k, e.Target[k], v)
		}
	}
}

func TestEmailAnchorScheduleAndHourlyLimit(t *testing.T) {
	tests := []struct {
		name    string
		last    time.Time
		changes changeWindow
		head    int64
		want    bool
	}{
		{name: "first anchor", head: 10, want: true},
		{name: "empty chain", head: 0},
		{name: "daily without admin changes", last: testNow.Add(-24 * time.Hour), head: 12, want: true},
		{name: "not daily yet", last: testNow.Add(-24*time.Hour + time.Second), head: 12},
		{name: "quiet burst", last: testNow.Add(-2 * time.Hour), changes: changeWindow{First: testNow.Add(-10 * time.Minute), Last: testNow.Add(-5 * time.Minute)}, head: 12, want: true},
		{name: "burst still active", last: testNow.Add(-2 * time.Hour), changes: changeWindow{First: testNow.Add(-10 * time.Minute), Last: testNow.Add(-time.Minute)}, head: 12},
		{name: "continuous admin traffic bounded", last: testNow.Add(-2 * time.Hour), changes: changeWindow{First: testNow.Add(-time.Hour), Last: testNow}, head: 12, want: true},
		{name: "hourly limit", last: testNow.Add(-time.Hour + time.Second), changes: changeWindow{First: testNow.Add(-10 * time.Minute), Last: testNow.Add(-5 * time.Minute)}, head: 12},
		{name: "hour boundary", last: testNow.Add(-time.Hour), changes: changeWindow{First: testNow.Add(-10 * time.Minute), Last: testNow.Add(-5 * time.Minute)}, head: 12, want: true},
		{name: "future previous timestamp", last: testNow.Add(time.Hour), changes: changeWindow{First: testNow.Add(-10 * time.Minute), Last: testNow.Add(-5 * time.Minute)}, head: 12},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var previous models.AuditEntry
			if !tc.last.IsZero() {
				previous = previousAnchor(tc.last)
			}
			p, err := plan("mx.example.test", "audit-anchor@example.test", []string{"owner@outside.test"}, testNow, audit.Anchor{Seq: tc.head, Hash: strings.Repeat("b", 64)}, previous, tc.changes)
			if err != nil {
				t.Fatal(err)
			}
			if (p != nil) != tc.want {
				t.Fatalf("publication = %v; want %v", p, tc.want)
			}
		})
	}
}

func TestEmailAnchorFirstAndRecipients(t *testing.T) {
	head := audit.Anchor{Seq: 1, Hash: strings.Repeat("b", 64)}
	p, err := plan("mx.example.test", "audit-anchor@example.test", []string{"a@outside.test", "b@outside.test", "a@outside.test"}, testNow, head, models.AuditEntry{}, changeWindow{})
	if err != nil || p == nil {
		t.Fatalf("plan: %v, %v", p, err)
	}
	if len(p.messages) != 2 {
		t.Fatalf("messages: %d", len(p.messages))
	}
	if p.entry.Target["previous_seq"] != "0" || p.entry.Target["previous_hash"] != audit.GenesisHash {
		t.Fatalf("first previous anchor: %v", p.entry.Target)
	}
	for _, q := range p.messages {
		if strings.Contains(q.RawMessage, "a@outside.test, b@outside.test") {
			t.Fatal("recipient list disclosed")
		}
	}
	p, err = plan("mx.example.test", "audit-anchor@example.test", nil, testNow, head, models.AuditEntry{}, changeWindow{})
	if err != nil || p != nil {
		t.Fatalf("no recipients: %v, %v", p, err)
	}
}

func TestEmailAnchorRejectsUnsafeOrCorruptInput(t *testing.T) {
	for _, tc := range []struct {
		name, server, from, to string
		previous               models.AuditEntry
	}{
		{name: "server injection", server: "mx.test\r\nBcc: attacker@test", from: "audit@test", to: "owner@outside.test"},
		{name: "server too long", server: strings.Repeat("x", 256), from: "audit@test", to: "owner@outside.test"},
		{name: "recipient injection", server: "mx.test", from: "audit@test", to: "owner@test\r\nBcc: attacker@test"},
		{name: "sender injection", server: "mx.test", from: "audit@test\r\nBcc: attacker@test", to: "owner@outside.test"},
		{name: "corrupt previous", server: "mx.test", from: "audit@test", to: "owner@outside.test", previous: models.AuditEntry{Seq: 2, OccurredAt: testNow.Add(-24 * time.Hour), Target: models.AuditTarget{"seq": "bad", "hash": "bad"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := plan(tc.server, tc.from, []string{tc.to}, testNow, audit.Anchor{Seq: 42, Hash: strings.Repeat("b", 64)}, tc.previous, changeWindow{})
			if err == nil || p != nil {
				t.Fatalf("unsafe input accepted: %v, %v", p, err)
			}
		})
	}
}

func TestEmailAnchorPreservesRemoteLocalPart(t *testing.T) {
	p, err := plan("mx.example.test", "audit-anchor@example.test", []string{"AnchorBox@EXAMPLE.test"}, testNow, audit.Anchor{Seq: 1, Hash: strings.Repeat("b", 64)}, models.AuditEntry{}, changeWindow{})
	if err != nil || p == nil {
		t.Fatalf("plan: %v, %v", p, err)
	}
	if p.messages[0].Recipient != "AnchorBox@example.test" {
		t.Fatalf("remote local part rewritten: %s", p.messages[0].Recipient)
	}
}
