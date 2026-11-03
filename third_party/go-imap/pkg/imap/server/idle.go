package server

import (
	"log/slog"
	"strings"
	"time"
)

// idlePollInterval is how often the IDLE poll goroutine re-reads the selected
// mailbox to announce new messages. It is a var, not a const, only so tests can
// shorten it; production always uses the default.
var idlePollInterval = 15 * time.Second

// handleIdle runs an IDLE command (RFC 2177) until the client sends DONE, the
// peer disconnects, or the inactivity autologout timer fires. It reports whether
// the caller must terminate the session: true when the read ended in error (an
// autologout or a peer disconnect) rather than a clean DONE. On an autologout it
// emits the untagged "* BYE" (RFC 3501 §5.4/§7.1.5) before returning so the
// caller's deferred Close is preceded by the announcement — the same contract the
// main command loop honours.
func (s *Session) handleIdle(tag string) (terminate bool) {
	if !s.auth.authenticated {
		s.tagged(tag, "NO", "Not authenticated")
		return false
	}
	if s.selected == nil {
		s.tagged(tag, "NO", "No mailbox selected")
		return false
	}

	s.send("+ idling")

	// Start polling goroutine for new messages. It owns s.writer and s.messages
	// for the duration of IDLE; the main goroutine only reads DONE and must not
	// touch either until the poll goroutine has fully stopped. `stopped` closes
	// when the goroutine returns — including after any in-flight tick — so the
	// main goroutine can wait for it before writing the tagged response.
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		defer s.recoverPanic() // a backend Messages panic here is a separate goroutine; contain it too
		interval := idlePollInterval
		if s.idlePoll > 0 {
			interval = s.idlePoll
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				messages, err := s.mailbox.Messages(s.selected.name)
				if err != nil {
					continue
				}
				s.reconcile(messages)
			}
		}
	}()

	// Wait for DONE from client, bounded by the same inactivity autologout timer
	// the main loop uses (RFC 3501 §5.4).
	_ = s.conn.SetDeadline(time.Now().Add(autologoutTimeout))
	var readErr error
	for !s.stopping() {
		line, tooLong, err := s.readLine()
		if err != nil {
			readErr = err
			break
		}
		if tooLong {
			continue // ignore over-long junk; keep waiting for DONE
		}
		if strings.ToUpper(line) == "DONE" {
			break
		}
	}

	// The poll goroutine owns s.writer during IDLE; stop it fully before this
	// goroutine writes anything (the tagged OK or the autologout BYE).
	close(done)
	<-stopped

	if s.stopping() {
		// The Server is stopping: the caller says BYE and closes.
		return true
	}
	if readErr != nil {
		// The read ended in error rather than a DONE. On an inactivity autologout
		// announce the close with an untagged BYE (RFC 3501 §5.4/§7.1.5) before the
		// caller closes the connection; a peer disconnect can receive nothing. Tell
		// the caller to terminate the session either way.
		if isAutologout(readErr) {
			slog.Info("imap: autologout on inactivity during IDLE", "remote", s.conn.RemoteAddr())
			s.sendAutologoutBye()
		} else {
			slog.Debug("imap: connection closed during IDLE", "remote", s.conn.RemoteAddr(), "error", readErr)
		}
		return true
	}

	s.tagged(tag, "OK", "IDLE terminated")
	return false
}
