package audit

import (
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/db/models"
)

func entryAt(seq int64) models.AuditEntry {
	id := uint(7)
	return models.AuditEntry{
		Seq:        seq,
		OccurredAt: time.Date(2026, 10, 17, 9, 30, 0, int(seq)*1000, time.UTC),
		ActorKind:  models.AuditActorAccount,
		ActorID:    &id,
		ActorName:  "admin",
		SessionID:  "0123456789abcdef0123456789abcdef",
		Action:     "admin.change",
		Outcome:    models.AuditOutcomeSuccess,
		Method:     "DELETE",
		Route:      "/api/v1/admin/domains/{id}",
		Status:     204,
		Target:     models.AuditTarget{"id": "3"},
		IP:         "192.0.2.10",
		UserAgent:  "restmail-test",
		RequestID:  "host/abc-000001",
	}
}

// chainOf builds a valid chain of n entries, as Append would.
func chainOf(n int) []models.AuditEntry {
	entries := make([]models.AuditEntry, n)
	prev := GenesisHash
	for i := range entries {
		e := entryAt(int64(i + 1))
		normalize(&e)
		e.PrevHash = prev
		e.Hash = Hash(&e)
		prev = e.Hash
		entries[i] = e
	}
	return entries
}

func TestHash_Deterministic(t *testing.T) {
	a, b := entryAt(1), entryAt(1)
	a.PrevHash, b.PrevHash = GenesisHash, GenesisHash
	if Hash(&a) != Hash(&b) {
		t.Fatal("equal entries hash differently")
	}
	if len(Hash(&a)) != 64 {
		t.Fatalf("hash %q is not hex SHA-256", Hash(&a))
	}

	// The same instant in another zone is the same entry.
	b.OccurredAt = a.OccurredAt.In(time.FixedZone("UTC+2", 2*3600))
	if Hash(&a) != Hash(&b) {
		t.Fatal("the hash depends on the time zone")
	}

	// The target's keys hash in sorted order, however the map was built.
	a.Target = models.AuditTarget{"id": "3", "eid": "9", "address": "x@example.com"}
	b.Target = models.AuditTarget{"address": "x@example.com", "eid": "9", "id": "3"}
	if Hash(&a) != Hash(&b) {
		t.Fatal("the hash depends on the target's key order")
	}

	// A missing target and an empty one are the same: {}.
	a.Target, b.Target = nil, models.AuditTarget{}
	if Hash(&a) != Hash(&b) {
		t.Fatal("a nil target and an empty one hash differently")
	}

	// A null id is not id 0.
	zero := uint(0)
	a.ActorID, b.ActorID = nil, &zero
	if Hash(&a) == Hash(&b) {
		t.Fatal("a null actor_id hashes like actor_id 0")
	}
}

// Every field is in the hash: changing any one changes it.
func TestHash_CoversEveryField(t *testing.T) {
	base := entryAt(1)
	base.PrevHash = GenesisHash
	other := uint(8)
	edits := map[string]func(e *models.AuditEntry){
		"seq":           func(e *models.AuditEntry) { e.Seq = 2 },
		"occurred_at":   func(e *models.AuditEntry) { e.OccurredAt = e.OccurredAt.Add(time.Microsecond) },
		"actor_kind":    func(e *models.AuditEntry) { e.ActorKind = models.AuditActorSystem },
		"actor_id":      func(e *models.AuditEntry) { e.ActorID = &other },
		"actor_name":    func(e *models.AuditEntry) { e.ActorName = "root" },
		"credential_id": func(e *models.AuditEntry) { e.CredentialID = &other },
		"session_id":    func(e *models.AuditEntry) { e.SessionID = "other" },
		"action":        func(e *models.AuditEntry) { e.Action = "auth.sign_in" },
		"outcome":       func(e *models.AuditEntry) { e.Outcome = models.AuditOutcomeDenied },
		"error_code":    func(e *models.AuditEntry) { e.ErrorCode = "forbidden" },
		"method":        func(e *models.AuditEntry) { e.Method = "PATCH" },
		"route":         func(e *models.AuditEntry) { e.Route = "/api/v1/admin/mailboxes/{id}" },
		"status":        func(e *models.AuditEntry) { e.Status = 403 },
		"target":        func(e *models.AuditEntry) { e.Target = models.AuditTarget{"id": "4"} },
		"ip":            func(e *models.AuditEntry) { e.IP = "192.0.2.11" },
		"user_agent":    func(e *models.AuditEntry) { e.UserAgent = "curl" },
		"request_id":    func(e *models.AuditEntry) { e.RequestID = "host/abc-000002" },
		"prev_hash":     func(e *models.AuditEntry) { e.PrevHash = strings.Repeat("1", 64) },
	}
	want := Hash(&base)
	for field, edit := range edits {
		e := entryAt(1)
		e.PrevHash = GenesisHash
		edit(&e)
		if Hash(&e) == want {
			t.Errorf("changing %s leaves the hash unchanged", field)
		}
	}
}

func TestNormalize_MakesEntriesStorable(t *testing.T) {
	e := entryAt(1)
	e.OccurredAt = time.Date(2026, 10, 17, 9, 30, 0, 123456789, time.FixedZone("UTC-5", -5*3600))
	e.UserAgent = strings.Repeat("ü", 600)
	e.ActorName = "bad\xffname\x00"
	e.Target = models.AuditTarget{"address": "a\x00b"}
	normalize(&e)

	if e.OccurredAt.Location() != time.UTC || e.OccurredAt.Nanosecond() != 123456000 {
		t.Errorf("occurred_at = %v, want UTC truncated to the microsecond", e.OccurredAt)
	}
	if n := len([]rune(e.UserAgent)); n != maxText {
		t.Errorf("user agent kept %d characters, want %d", n, maxText)
	}
	if e.ActorName != "bad�name�" {
		t.Errorf("actor name = %q: invalid UTF-8 and NUL must be replaced", e.ActorName)
	}
	if e.Target["address"] != "a�b" {
		t.Errorf("target = %q: NUL must be replaced", e.Target["address"])
	}
}

func TestVerify_ValidChain(t *testing.T) {
	res := Verify(chainOf(5))
	if !res.OK || res.Checked != 5 || res.FirstSeq != 1 || res.LastSeq != 5 || res.BrokenAtSeq != nil {
		t.Fatalf("valid chain: %+v", res)
	}
	if res.LastHash != chainOf(5)[4].Hash {
		t.Errorf("last_hash = %s, want the fifth entry's", res.LastHash)
	}

	empty := Verify(nil)
	if !empty.OK || empty.Checked != 0 || empty.LastHash != GenesisHash {
		t.Fatalf("empty log: %+v", empty)
	}
}

// Every way of tampering with the chain is caught, at the entry where it
// shows; checked counts the entries that held before it.
func TestVerify_CatchesTampering(t *testing.T) {
	cases := []struct {
		name    string
		tamper  func([]models.AuditEntry) []models.AuditEntry
		broken  int64
		checked int64
	}{
		{"an edited field", func(es []models.AuditEntry) []models.AuditEntry {
			es[2].ActorName = "someone else"
			return es
		}, 3, 2},
		{"an edited field, its hash recomputed", func(es []models.AuditEntry) []models.AuditEntry {
			es[2].Outcome = models.AuditOutcomeSuccess
			es[2].Status = 200
			es[2].Hash = Hash(&es[2])
			return es
		}, 4, 3},
		{"a deleted entry", func(es []models.AuditEntry) []models.AuditEntry {
			return append(es[:2], es[3:]...)
		}, 4, 2},
		{"a deleted entry, the rest renumbered and rehashed", func(es []models.AuditEntry) []models.AuditEntry {
			es = append(es[:2], es[3:]...)
			for i := 2; i < len(es); i++ {
				es[i].Seq--
				es[i].Hash = Hash(&es[i])
			}
			return es
		}, 3, 2},
		{"an inserted entry", func(es []models.AuditEntry) []models.AuditEntry {
			extra := entryAt(3)
			extra.ActorName = "inserted"
			extra.PrevHash = es[1].Hash
			extra.Hash = Hash(&extra)
			return append(es[:2], append([]models.AuditEntry{extra}, es[2:]...)...)
		}, 3, 3},
		{"an inserted entry, the rest renumbered and rehashed", func(es []models.AuditEntry) []models.AuditEntry {
			extra := entryAt(3)
			extra.ActorName = "inserted"
			extra.PrevHash = es[1].Hash
			extra.Hash = Hash(&extra)
			es = append(es[:2], append([]models.AuditEntry{extra}, es[2:]...)...)
			for i := 3; i < len(es); i++ {
				es[i].Seq++
				es[i].Hash = Hash(&es[i])
			}
			return es
		}, 4, 3},
		{"two entries swapped", func(es []models.AuditEntry) []models.AuditEntry {
			es[1], es[2] = es[2], es[1]
			return es
		}, 3, 1},
		{"two entries swapped, seqs left in order", func(es []models.AuditEntry) []models.AuditEntry {
			es[1], es[2] = es[2], es[1]
			es[1].Seq, es[2].Seq = 2, 3
			return es
		}, 2, 1},
		{"a wrong prev_hash, its hash recomputed", func(es []models.AuditEntry) []models.AuditEntry {
			es[3].PrevHash = strings.Repeat("f", 64)
			es[3].Hash = Hash(&es[3])
			return es
		}, 4, 3},
		{"a first entry that does not start from the genesis hash", func(es []models.AuditEntry) []models.AuditEntry {
			es[0].PrevHash = strings.Repeat("a", 64)
			es[0].Hash = Hash(&es[0])
			return es
		}, 1, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := Verify(tc.tamper(chainOf(5)))
			if res.OK {
				t.Fatalf("tampering not caught: %+v", res)
			}
			if res.BrokenAtSeq == nil || *res.BrokenAtSeq != tc.broken {
				t.Fatalf("broken at %v, want seq %d (%s)", res.BrokenAtSeq, tc.broken, res.Reason)
			}
			if res.Reason == "" {
				t.Error("no reason given")
			}
			if res.Checked != tc.checked {
				t.Errorf("%d entries held before the break, want %d", res.Checked, tc.checked)
			}
		})
	}
}

// A log whose first entries are gone (by retention, later) is checked from
// the first entry left, and says so.
func TestVerify_LogStartingAboveOne(t *testing.T) {
	res := Verify(chainOf(6)[2:])
	if !res.OK || res.FirstSeq != 3 || res.LastSeq != 6 || res.Checked != 4 {
		t.Fatalf("log from seq 3: %+v", res)
	}
	if res.Note == "" {
		t.Error("a check that does not start at seq 1 must say so")
	}
}
