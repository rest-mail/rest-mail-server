package server

import (
	"errors"
	"strings"
	"testing"

	"github.com/rest-mail/go-pop3/pkg/pop3"
)

// loadErrMailbox authenticates fine but fails to enumerate its maildrop, as when
// the underlying store is unreachable at maildrop-open time (RFC 1939 §4). Unlike
// panicMailbox (issue #6) it returns a plain error rather than panicking.
type loadErrMailbox struct{}

func (loadErrMailbox) Messages() ([]pop3.Message, error) {
	return nil, errors.New("maildrop unavailable")
}
func (loadErrMailbox) Retrieve(string) ([]byte, error) { return nil, nil }
func (loadErrMailbox) MarkSeen(string) error           { return nil }
func (loadErrMailbox) Delete(string) error             { return nil }

// loadErrBackend authenticates successfully but hands out a mailbox whose
// Messages() load fails.
type loadErrBackend struct{}

func (loadErrBackend) Authenticate(string, string) (pop3.Mailbox, error) {
	return loadErrMailbox{}, nil
}

// TestPOP3_MessagesLoadFailureStaysInAuthorization proves that when the maildrop
// cannot be opened (Messages() returns an error), PASS is answered with -ERR and
// the session remains in AUTHORIZATION so the client can retry — rather than
// being wedged in TRANSACTION with a phantom empty maildrop (issue #7, RFC 1939
// §4).
func TestPOP3_MessagesLoadFailureStaysInAuthorization(t *testing.T) {
	h := newRawHarness(t, loadErrBackend{})

	if got := h.cmd("USER alice"); !strings.HasPrefix(got, "+OK") {
		t.Fatalf("USER = %q, want +OK", got)
	}
	// PASS opens the maildrop; the load fails, so it must be reported -ERR.
	if got := h.cmd("PASS secret"); !strings.HasPrefix(got, "-ERR") {
		t.Errorf("PASS with failing Messages() = %q, want -ERR", got)
	}

	// The session must NOT have entered TRANSACTION: a command that requires
	// authentication is refused. On the buggy code the session is already marked
	// authenticated before the failed load, so STAT wrongly returns "+OK 0 0"
	// over a phantom empty maildrop.
	if got := h.cmd("STAT"); !strings.HasPrefix(got, "-ERR") {
		t.Errorf("STAT after failed maildrop load = %q, want -ERR (still in AUTHORIZATION)", got)
	}

	// The client must be able to retry authentication; USER must not be refused
	// with "Already authenticated".
	if got := h.cmd("USER alice"); !strings.HasPrefix(got, "+OK") {
		t.Errorf("USER retry after failed load = %q, want +OK (client can re-auth)", got)
	}
}
