package rfctest

import (
	"errors"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	pop3 "github.com/rest-mail/go-pop3/pkg/pop3"
)

type authPipelineCall struct{ operation, user, pass string }

// Record actual backend calls independently of the wire state. The lock also
// protects observations on failure paths while the session goroutine runs.
type authPipelineBackend struct {
	mu       sync.Mutex
	calls    []authPipelineCall
	accepted bool
}

func (b *authPipelineBackend) Authenticate(user, pass string) (pop3.Mailbox, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls = append(b.calls, authPipelineCall{"authenticate", user, pass})
	b.accepted = user == "alice" && pass == "secret"
	if !b.accepted {
		return nil, errors.New("invalid credentials")
	}
	return b, nil
}
func (b *authPipelineBackend) Messages() ([]pop3.Message, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls = append(b.calls, authPipelineCall{operation: "messages"})
	if !b.accepted {
		return nil, errors.New("enumeration before accepted credentials")
	}
	return []pop3.Message{{UID: "auth-pipeline-one", Size: 42}}, nil
}
func (b *authPipelineBackend) record(operation string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls = append(b.calls, authPipelineCall{operation: operation})
}
func (b *authPipelineBackend) Retrieve(string) ([]byte, error) {
	b.record("retrieve")
	return nil, errors.New("unexpected retrieval")
}
func (b *authPipelineBackend) MarkSeen(string) error { b.record("seen"); return nil }
func (b *authPipelineBackend) Delete(string) error   { b.record("delete"); return nil }
func (b *authPipelineBackend) expectCalls(t *testing.T, want ...authPipelineCall) {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	if !reflect.DeepEqual(b.calls, want) {
		t.Fatalf("backend calls = %v, want %v", b.calls, want)
	}
}

// Unlike pipeline, this helper makes no assumption that authentication already
// succeeded. The complete <=512-byte write must return before ANY batch reply
// is read. It fits the public Session's 1024-byte reader; a deadline bounds a
// regression that would otherwise deadlock synchronous net.Pipe writes.
func authPipeline(t *testing.T, w *wire, commands, replies []string) {
	t.Helper()
	batch := strings.Join(commands, "\r\n") + "\r\n"
	if len(batch) > 512 {
		t.Fatal("authentication pipeline exceeds bounded batch size")
	}
	if err := w.conn.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if n, err := io.WriteString(w.conn, batch); err != nil || n != len(batch) {
		t.Fatalf("send complete authentication batch: wrote %d/%d bytes: %v", n, len(batch), err)
	}
	for i, want := range replies {
		if got := w.line(); got != want {
			t.Fatalf("authentication batch reply line %d = %q, want %q", i+1, got, want)
		}
	}
}

// RFC 2449 §6.6 explicitly permits batching USER/PASS. Exact explanatory text
// pins current output; the RFC requirement here is ordered command processing.
// checklist: POP3-PIPELINING-06
func TestAuthPipeliningSuccess(t *testing.T) {
	b := &authPipelineBackend{}
	w := connect(t, b) // Public NewSession, nil TLS/limiter, memory and net.Pipe only.
	authPipeline(t, w, []string{
		"CAPA", "STAT", "USER alice", "PASS secret", "STAT", "CAPA", "NOOP",
	}, []string{
		"+OK Capability list follows", "USER", "TOP", "UIDL", "RESP-CODES", "AUTH-RESP-CODE", "PIPELINING", ".",
		"-ERR Not authenticated", "+OK", "+OK Authentication successful", "+OK 1 42",
		"+OK Capability list follows", "USER", "TOP", "UIDL", "RESP-CODES", "AUTH-RESP-CODE", "PIPELINING", ".", "+OK",
	})
	// Fresh commands detect extra replies, unconsumed CAPA dots or lost state.
	expect(t, w.cmd("STAT"), "+OK 1 42")
	expect(t, w.cmd("NOOP"), "+OK")
	quit(t, w, "+OK") // EOF and joined Handle; no backend deletion is expected.
	b.expectCalls(t, authPipelineCall{"authenticate", "alice", "secret"}, authPipelineCall{operation: "messages"})
}

// Retry/reset behavior is existing library policy, separately mapped from the
// normative ordering proof. This makes no strict immediate-PASS sequencing claim.
// checklist: POP3-PIPELINING-07 POP3-PIPELINING-POLICY-01
func TestAuthPipeliningFailureRetry(t *testing.T) {
	b := &authPipelineBackend{}
	w := connect(t, b)
	authPipeline(t, w, []string{
		"USER alice", "PASS wrong", "STAT", "PASS secret", "CAPA",
		"USER alice", "PASS secret", "STAT", "NOOP",
	}, []string{
		"+OK", "-ERR [AUTH] Authentication failed", "-ERR Not authenticated", "-ERR USER first",
		"+OK Capability list follows", "USER", "TOP", "UIDL", "RESP-CODES", "AUTH-RESP-CODE", "PIPELINING", ".",
		"+OK", "+OK Authentication successful", "+OK 1 42", "+OK",
	})
	expect(t, w.cmd("STAT"), "+OK 1 42")
	expect(t, w.cmd("NOOP"), "+OK")
	quit(t, w, "+OK")
	// No Authenticate for the reset USER's PASS; no Messages before success;
	// no retrieval, mark-seen or deletion. Order and exact arguments matter.
	b.expectCalls(t,
		authPipelineCall{"authenticate", "alice", "wrong"},
		authPipelineCall{"authenticate", "alice", "secret"},
		authPipelineCall{operation: "messages"},
	)
}
