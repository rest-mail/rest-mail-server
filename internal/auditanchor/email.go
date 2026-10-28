// Package auditanchor publishes small copies of the audit chain head by email.
// The durable outbound queue handles delivery, retries and DKIM signing.
package auditanchor

import (
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/restmail/restmail/internal/audit"
	"github.com/restmail/restmail/internal/db/models"
	rmail "github.com/restmail/restmail/internal/mail"
)

const emailAction = "audit.anchor.email"
const quietPeriod = 5 * time.Minute
const minimumInterval = time.Hour
const dailyInterval = 24 * time.Hour

type changeWindow struct{ First, Last time.Time }
type publication struct {
	messages []models.OutboundQueue
	entry    models.AuditEntry
}

// plan has no access to log entries: only the head, the previous publication
// and the time window of administrative changes can enter an anchor message.
func plan(server, sender string, recipients []string, now time.Time, head audit.Anchor, previous models.AuditEntry, changes changeWindow) (*publication, error) {
	if rmail.ContainsControlChar(server) || len(server) == 0 || len(server) > 255 {
		return nil, fmt.Errorf("invalid audit anchor server name")
	}
	sender, err := rmail.BareAddress(sender)
	if err != nil {
		return nil, fmt.Errorf("audit anchor sender: %w", err)
	}
	last, err := previousHead(previous)
	if err != nil {
		return nil, err
	}
	if head.Seq == 0 || len(recipients) == 0 {
		return nil, nil
	}
	if !validHash(head.Hash) {
		return nil, fmt.Errorf("invalid audit chain head")
	}
	if previous.Seq > 0 {
		age := now.Sub(previous.OccurredAt)
		if age < minimumInterval {
			return nil, nil
		}
		burst := !changes.First.IsZero() && (now.Sub(changes.Last) >= quietPeriod || now.Sub(changes.First) >= minimumInterval)
		if age < dailyInterval && !burst {
			return nil, nil
		}
	}
	now = now.UTC().Truncate(time.Microsecond)
	body := fmt.Sprintf("REST Mail audit anchor\r\nServer: %s\r\nTime: %s\r\nSeq: %d\r\nHash: %s\r\nPrevious-Seq: %d\r\nPrevious-Hash: %s\r\n", server, now.Format(time.RFC3339Nano), head.Seq, head.Hash, last.Seq, last.Hash)
	p := &publication{entry: models.AuditEntry{
		OccurredAt: now, ActorKind: models.AuditActorSystem, Action: emailAction, Outcome: models.AuditOutcomeSuccess,
		Target: models.AuditTarget{"seq": strconv.FormatInt(head.Seq, 10), "hash": head.Hash, "previous_seq": strconv.FormatInt(last.Seq, 10), "previous_hash": last.Hash, "delivery": "queued"},
	}}
	seen := map[string]bool{}
	for _, recipient := range recipients {
		recipient, err = rmail.BareAddress(recipient)
		if err != nil {
			return nil, fmt.Errorf("audit anchor recipient: %w", err)
		}
		if seen[recipient] {
			continue
		}
		seen[recipient] = true
		domain := recipient[strings.LastIndexByte(recipient, '@')+1:]
		raw := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: REST Mail audit anchor\r\nDate: %s\r\nMessage-ID: <audit-anchor-%d-%d@%s>\r\nAuto-Submitted: auto-generated\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n%s", sender, recipient, now.Format(time.RFC1123Z), head.Seq, now.UnixMicro(), sender[strings.LastIndexByte(sender, '@')+1:], body)
		p.messages = append(p.messages, models.OutboundQueue{Sender: sender, Recipient: recipient, Domain: domain, RawMessage: raw, Status: "pending", NextAttempt: now})
	}
	return p, nil
}

func validHash(hash string) bool {
	if len(hash) != 64 {
		return false
	}
	_, err := hex.DecodeString(hash)
	return err == nil
}

func previousHead(previous models.AuditEntry) (audit.Anchor, error) {
	if previous.Seq == 0 {
		return audit.Anchor{Hash: audit.GenesisHash}, nil
	}
	seq, err := strconv.ParseInt(previous.Target["seq"], 10, 64)
	if err != nil || seq < 1 || seq >= previous.Seq || !validHash(previous.Target["hash"]) {
		return audit.Anchor{}, fmt.Errorf("invalid previous email anchor at audit seq %d", previous.Seq)
	}
	return audit.Anchor{Seq: seq, Hash: previous.Target["hash"]}, nil
}
