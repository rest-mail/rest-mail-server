package rfctest

import (
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

// Send the entire bounded batch before reading any reply. net.Pipe writes are
// synchronous: keep the batch below Session's 1024-byte command-reader buffer
// so its first read can accept the whole write before it starts writing replies.
// This exercises batching, not transport-window limits or arbitrary batch sizes.
func pipeline(t *testing.T, w *wire, commands []string, replies []string) {
	t.Helper()
	batch := strings.Join(commands, "\r\n") + "\r\n"
	if len(batch) > 512 {
		t.Fatal("pipeline fixture exceeds bounded batch size")
	}
	if err := w.conn.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if n, err := io.WriteString(w.conn, batch); err != nil || n != len(batch) {
		t.Fatalf("send complete batch: wrote %d/%d bytes: %v", n, len(batch), err)
	}
	for i, want := range replies {
		if got := w.line(); got != want {
			t.Fatalf("batch %q, reply line %d = %q, want %q", commands, i+1, got, want)
		}
	}
	// A separate command exposes an extra reply or unconsumed dot terminator.
	expect(t, w.cmd("NOOP"), "+OK")
}

// checklist: POP3-PIPELINING-01
func TestPipeliningCapabilities(t *testing.T) {
	m := &optionalMailbox{transactionMailbox: transactionMailbox{raw: transactionMessages}, uids: updateUIDs}
	w := connect(t, m)
	stlsCapabilities(t, w, false) // Includes exactly parameterless PIPELINING.
	w.login("secret")
	stlsCapabilities(t, w, false)
	expect(t, w.cmd("NOOP"), "+OK")
}

// checklist: POP3-PIPELINING-02
func TestPipeliningSingleReplies(t *testing.T) {
	w := optional(t, transactionMessages, updateUIDs)
	for range 3 {
		pipeline(t, w, []string{"NOOP", "STAT", "LIST 1", "UIDL 1"}, []string{
			"+OK", fmt.Sprintf("+OK 3 %d", total()),
			fmt.Sprintf("+OK 1 %d", len(transactionMessages[0])), "+OK 1 uid-alpha",
		})
	}
}

// checklist: POP3-PIPELINING-03
func TestPipeliningMultilineReplies(t *testing.T) {
	w := optional(t, transactionMessages, updateUIDs)
	for range 2 {
		pipeline(t, w, []string{"LIST", "TOP 1 0", "RETR 1", "NOOP"}, []string{
			fmt.Sprintf("+OK 3 messages (%d octets)", total()),
			fmt.Sprintf("1 %d", len(transactionMessages[0])),
			fmt.Sprintf("2 %d", len(transactionMessages[1])),
			fmt.Sprintf("3 %d", len(transactionMessages[2])), ".",
			"+OK", "Subject: a", "", ".",
			fmt.Sprintf("+OK %d octets", len(transactionMessages[0])),
			"Subject: a", "", "..", "...", "..x", ".", "+OK",
		})
	}
}

// checklist: POP3-PIPELINING-04
func TestPipeliningErrorRecovery(t *testing.T) {
	w := optional(t, transactionMessages, updateUIDs)
	for range 2 {
		pipeline(t, w, []string{"STAT unexpected", "NOOP", "LIST 0", "UIDL 1", "WHAT", "STAT"}, []string{
			"-ERR Command accepts no arguments", "+OK", "-ERR No such message",
			"+OK 1 uid-alpha", "-ERR Unknown command", fmt.Sprintf("+OK 3 %d", total()),
		})
	}
}

// checklist: POP3-PIPELINING-05
func TestPipeliningStateOrder(t *testing.T) {
	w := optional(t, transactionMessages, updateUIDs)
	for range 3 {
		pipeline(t, w, []string{"DELE 1", "STAT", "UIDL 1", "UIDL 2", "RSET", "STAT", "UIDL 1"}, []string{
			"+OK Message 1 deleted", fmt.Sprintf("+OK 2 %d", total()-len(transactionMessages[0])),
			"-ERR Message is deleted", "+OK 2 uid-bravo", "+OK Maildrop has 3 messages",
			fmt.Sprintf("+OK 3 %d", total()), "+OK 1 uid-alpha",
		})
	}
}
