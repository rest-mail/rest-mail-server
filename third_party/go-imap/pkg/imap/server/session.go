package server

import (
	"bufio"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/rest-mail/go-imap/pkg/imap"
	"github.com/rest-mail/go-imap/pkg/imap/internal/wire"
)

// Session represents a single IMAP conversation with a client.
type Session struct {
	conn      net.Conn
	reader    *bufio.Reader
	writer    *bufio.Writer
	backend   imap.Backend
	hostname  string
	tlsConfig *tls.Config
	limiter   imap.Limiter
	idlePoll  time.Duration // SetIdlePollInterval; zero is idlePollInterval
	// stop is the Server's shutdown channel, closed when it begins to stop; nil for a
	// Session no Server started.
	stop    <-chan struct{}
	saidBye bool // an untagged BYE has been sent

	// Session state
	usingTLS     bool
	auth         *authState
	mailbox      imap.Mailbox
	selected     *selectedMailbox
	readOnly     bool           // true when the selection was opened via EXAMINE (RFC 3501 §6.3.2)
	messages     []imap.Message // cached message list for current selection
	keywordNames map[string]string
	deleted      map[uint32]bool // UIDs flagged \Deleted in this session
	// saved is the search result variable SEARCH RETURN (SAVE) sets and "$"
	// names (RFC 5182 §2.1), as UIDs, so an expunge cannot shift it; a UID no
	// longer in the view matches nothing.
	saved []uint32
	utf8  bool // ENABLE UTF8=ACCEPT succeeded (RFC 6855)
}

type authState struct {
	authenticated bool
}

type selectedMailbox struct {
	name   string
	total  int64
	unread int64
}

// MaxCommandLineLength bounds a single protocol line read from the client —
// the command line in the main loop, the AUTHENTICATE continuation, the IDLE
// DONE line, and the CRLF trailing an APPEND literal. RFC 3501 sizes command
// lines in the low thousands of octets; 8 KiB leaves generous headroom while
// preventing an unauthenticated client from streaming an unbounded "line" (no
// terminator) to exhaust process memory. It is a package variable so operators
// can tune it.
//
// This cap does NOT limit message payloads: those arrive as length-prefixed
// IMAP literals (e.g. APPEND's {N}), which are read with io.ReadFull against a
// separately enforced size bound and never flow through readLine.
var MaxCommandLineLength = 8 * 1024

// MaxLiteralSize bounds a synchronizing (or LITERAL+ non-synchronizing) literal
// used as an ordinary string/astring command argument — a literal mailbox name,
// LOGIN credential, SEARCH key, etc. (RFC 3501 §4.3). Such arguments are short by
// nature; 8 KiB is far more than any real name or credential needs while
// preventing an unauthenticated client from declaring a huge {N} to make the
// server allocate and read unbounded octets before authenticating. The declared
// size is checked BEFORE the "+" continuation is sent, so an over-large literal
// is refused without its octets ever being invited. It is a package variable so
// operators can tune it.
//
// This cap is deliberately independent of — and much smaller than — APPEND's own
// message-literal limit: APPEND carries whole messages and keeps its separate,
// larger bound on its dedicated read path (handleAppend), which this general
// argument-literal handling never touches.
var MaxLiteralSize = 8 * 1024

// nonSyncTooBig is the refusal of a non-synchronizing literal over
// maxNonSyncLiteral, as the tagged BAD and the BYE that follows it say it.
var nonSyncTooBig = fmt.Sprintf("[TOOBIG] Non-synchronizing literal over %d bytes; send it as a synchronizing literal", maxNonSyncLiteral)

// maxNonSyncLiteral is the largest non-synchronizing literal ({N+}) a client may
// send: the server advertises LITERAL-, so a larger one "MUST be sent as a
// synchronizing literal" (RFC 7888 §5).
const maxNonSyncLiteral = 4096

// readLine reads one CRLF/LF-terminated line from the client and returns it with
// any trailing CR/LF stripped, enforcing MaxCommandLineLength on the line length
// (excluding the terminator).
//
// If the line exceeds the cap, readLine stops accumulating, drains the remainder
// up to and including the next '\n' without retaining it, and returns
// tooLong=true — so at most one buffer chunk beyond the cap is ever held, no
// matter how much the client streams. Callers reject the command with a BAD
// response instead of buffering the whole line. A read error (including EOF) is
// returned in err with whatever partial line was read.
func (s *Session) readLine() (line string, tooLong bool, err error) {
	line, tooLong, _, err = s.readCommandLine()
	return line, tooLong, err
}

// readCommandLine is readLine that also reports, for a discarded over-long
// line, whether it ended in "+}" — a possible LITERAL+ marker whose octets are
// already in flight (RFC 7888 §3). Only the last few raw octets are kept, so the
// check costs no extra memory however long the line is.
func (s *Session) readCommandLine() (line string, tooLong, nonSyncLiteral bool, err error) {
	var buf []byte
	for {
		// ReadSlice yields chunks bounded by the reader's buffer, so a copy into
		// buf below is required before the next read invalidates chunk.
		chunk, e := s.reader.ReadSlice('\n')
		buf = append(buf, chunk...)

		if len(buf) > MaxCommandLineLength {
			// Over the cap: stop accumulating (buf is discarded on return, so at
			// most one chunk beyond the cap is ever held) and resync the stream.
			tail := lineTail(nil, buf)
			if e == nil {
				// Terminator already consumed; stream is at the next line.
				return "", true, endsNonSyncLiteral(tail), nil
			}
			if errors.Is(e, bufio.ErrBufferFull) {
				tail, de := s.discardToLineEnd(tail)
				if de != nil {
					return "", true, false, de
				}
				return "", true, endsNonSyncLiteral(tail), nil
			}
			return "", true, false, e // read error before any terminator
		}

		if e == nil {
			return strings.TrimRight(string(buf), "\r\n"), false, false, nil
		}
		if errors.Is(e, bufio.ErrBufferFull) {
			continue // more of the same (still-within-cap) line
		}
		// Real read error / EOF, possibly with a partial final line.
		return strings.TrimRight(string(buf), "\r\n"), false, false, e
	}
}

// discardToLineEnd consumes bytes up to and including the next '\n' without
// retaining them, resynchronising the stream after an over-long line in
// constant memory. Work is bounded by the connection's read deadline. It
// returns the line's last few octets, continuing from tail.
func (s *Session) discardToLineEnd(tail []byte) ([]byte, error) {
	for {
		chunk, err := s.reader.ReadSlice('\n')
		tail = lineTail(tail, chunk)
		if err == nil {
			return tail, nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return tail, err
	}
}

// lineTail keeps the last four octets of tail followed by chunk: enough for a
// "+}" marker plus its CRLF terminator.
func lineTail(tail, chunk []byte) []byte {
	const keep = 4
	if len(chunk) >= keep {
		return append([]byte(nil), chunk[len(chunk)-keep:]...)
	}
	tail = append(tail, chunk...)
	if len(tail) > keep {
		tail = append([]byte(nil), tail[len(tail)-keep:]...)
	}
	return tail
}

// endsNonSyncLiteral reports whether a line tail ends in "+}" before its
// terminator.
func endsNonSyncLiteral(tail []byte) bool {
	return strings.HasSuffix(strings.TrimRight(string(tail), "\r\n"), "+}")
}

// readLiteralArgs materializes any synchronizing (or LITERAL+ non-synchronizing)
// literals embedded in a command's argument text and returns the fully assembled
// arguments (RFC 3501 §4.3). A literal — "{n}" / "{n+}" at the end of a line —
// may stand in for any string/astring argument of any command, so this runs for
// every command except APPEND (whose terminal message literal is read on its own
// large-message path in handleAppend).
//
// For each trailing literal it: bounds the declared size against MaxLiteralSize
// (before any continuation, so an over-large literal's octets are never invited);
// sends a "+" continuation for a synchronizing literal (a LITERAL+ "{n+}" needs
// none — its octets are already in flight); reads exactly n octets as the
// argument value; reads the remainder of the command line that follows the
// octets; and splices the octets back in place of the marker, re-encoded as a
// quoted-string so the per-command parsers consume them unchanged (an embedded
// quote/backslash is escaped; other 8-bit content, including CR/LF/NUL, is
// preserved verbatim for those parsers to validate). The spliced-in continuation
// may itself end in another literal, so this repeats until the assembled line
// carries no trailing literal.
//
// ok is false after a tagged BAD for a resource limit, or a read error/EOF;
// the caller must not dispatch the command. closeConn also tells the caller to
// end the session when unread literal bytes or a lost line boundary make the
// stream unsafe to reuse. Refusals with unread non-synchronizing literals send
// BYE instead of draining potentially large payloads (RFC 7888 §§3–4).
// Memory stays bounded:
// the aggregate literal octets are capped at MaxLiteralSize and the assembled
// line at MaxCommandLineLength, both independent of how the client chunks input.
func (s *Session) readLiteralArgs(tag, args string) (assembled string, ok, closeConn bool) {
	reject := func(reason string, fatal bool) (string, bool, bool) {
		s.tagged(tag, "BAD", reason)
		if fatal {
			s.send("* BYE Command literal rejected; stream cannot be reused")
		}
		return "", false, fatal
	}
	total := 0    // aggregate literal octets across this command
	typed := args // what the client typed, before any literal is spliced in
	for {
		prefix, size, sync, isLit := wire.SplitTrailingLiteral(args)
		// Validate only typed text, before inviting a synchronizing literal.
		// Literal octets already spliced into args remain exempt.
		if reason := s.commandTextError(typed); reason != "" {
			return reject(reason, isLit && !sync)
		}
		if !isLit {
			return args, true, false // no (further) trailing literal
		}

		// A non-synchronizing literal over LITERAL-'s limit has its octets in
		// flight: refuse it and end the session, with TOOBIG (RFC 7888 §§4-5).
		if !sync && size > maxNonSyncLiteral {
			s.tagged(tag, "BAD", nonSyncTooBig)
			s.send("* BYE %s", nonSyncTooBig)
			return "", false, true
		}
		// Bound the literal BEFORE soliciting its octets. A synchronizing literal
		// is refused without a continuation, so the client never sends the octets.
		if size > MaxLiteralSize || total+size > MaxLiteralSize {
			return reject(fmt.Sprintf("Literal too large (limit %d bytes)", MaxLiteralSize), !sync)
		}
		total += size

		if sync {
			s.send("+ Ready for literal data")
		}

		buf := make([]byte, size)
		if _, err := io.ReadFull(s.reader, buf); err != nil {
			return "", false, true // never dispatch buffered partial input after a read error
		}

		// The command continues on the line following the literal octets.
		cont, tooLong, err := s.readLine()
		if err != nil {
			return "", false, true
		}
		if tooLong {
			// readLine discarded the suffix, including any next literal marker.
			return reject(fmt.Sprintf("Command line too long (limit %d bytes)", MaxCommandLineLength), true)
		}

		typed = cont
		args = prefix + wire.QuoteLiteral(buf) + cont
		if len(args) > MaxCommandLineLength {
			// Only the continuation can introduce another unread literal. A
			// synchronizing marker has not invited bytes; a complete command
			// without a marker has already consumed its final CRLF.
			_, _, nextSync, nextLiteral := wire.SplitTrailingLiteral(cont)
			return reject(fmt.Sprintf("Command too long (limit %d bytes)", MaxCommandLineLength), nextLiteral && !nextSync)
		}
	}
}

// NewSession creates an IMAP session over conn, authenticating against backend.
// hostname is announced in the greeting. A nil limiter defaults to [NopLimiter].
// Call [Session.Handle] to run it.
func NewSession(conn net.Conn, backend imap.Backend, hostname string, tlsConfig *tls.Config, limiter imap.Limiter) *Session {
	if limiter == nil {
		limiter = imap.NopLimiter{}
	}
	return &Session{
		conn:      conn,
		reader:    bufio.NewReader(conn),
		writer:    bufio.NewWriter(conn),
		backend:   backend,
		hostname:  hostname,
		tlsConfig: tlsConfig,
		limiter:   limiter,
		auth:      &authState{},
		deleted:   make(map[uint32]bool),
	}
}

// recoverPanic contains a panic raised anywhere in a single client's serve
// goroutine — typically a bug in a backend Mailbox method or a handler — so that
// one failing session cannot crash the process and drop every other connection.
// It logs the panic with a stack trace, tells the client the server is closing
// this connection (untagged BYE, RFC 3501 §7.1.5), and closes the connection so
// a recovered panic never leaves a hung socket. It must be deferred at the top
// of every goroutine that serves or polls one client (the main [Session.Handle]
// loop and the IDLE poll goroutine).
func (s *Session) recoverPanic() {
	if r := recover(); r != nil {
		slog.Error("imap: recovered panic in session; closing connection",
			"remote", s.conn.RemoteAddr(),
			"panic", r,
			"stack", string(debug.Stack()),
		)
		// Best-effort graceful failure; the close below guarantees the socket is
		// torn down even if this write fails.
		s.send("* BYE Internal server error, closing connection")
		_ = s.conn.Close()
	}
}

// autologoutTimeout is the inactivity window after which the server autologs the
// client out (RFC 3501 §5.4). The RFC requires this timer to be at least 30
// minutes, so production must never shorten it; it is a var, not a const, only so
// tests can drive the autologout path quickly — the same seam idlePollInterval
// uses. When it fires, [Session.Handle] and [Session.handleIdle] announce the
// close with an untagged "* BYE" (§7.1.5) before dropping the connection.
var autologoutTimeout = 30 * time.Minute

// isAutologout reports whether err is the inactivity read-deadline expiry
// (autologout) rather than a peer disconnect or other read error — the only case
// that warrants sending a "* BYE" before closing, since a disconnected peer can
// receive nothing. A net.Conn (and net.Pipe) read past its deadline returns
// os.ErrDeadlineExceeded, which readLine surfaces unwrapped.
// stopping reports whether the Server that started this session has begun to stop.
func (s *Session) stopping() bool {
	select {
	case <-s.stop:
		return true
	default:
		return false
	}
}

// sendShutdownBye announces that the server is shutting down (RFC 3501 §§3.4, 7.1.5),
// under the same write grace as the autologout BYE.
func (s *Session) sendShutdownBye() {
	_ = s.conn.SetWriteDeadline(time.Now().Add(autologoutByeGrace))
	s.send("* BYE Server shutting down")
}

func isAutologout(err error) bool {
	return errors.Is(err, os.ErrDeadlineExceeded)
}

// autologoutByeGrace bounds how long the autologout BYE write may block. The
// inactivity deadline that just fired also governs writes (net.Conn.SetDeadline
// sets both directions), so a fresh write deadline is required or the BYE would
// itself time out and never leave the buffer; the grace is short so a wedged
// client cannot pin the serve goroutine.
var autologoutByeGrace = 10 * time.Second

// sendAutologoutBye announces the inactivity autologout with an untagged BYE
// (RFC 3501 §5.4/§7.1.5) after refreshing the write deadline the expired
// inactivity deadline left in the past, then leaves the caller to close the
// connection.
func (s *Session) sendAutologoutBye() {
	_ = s.conn.SetWriteDeadline(time.Now().Add(autologoutByeGrace))
	s.send("* BYE Autologout; idle for too long")
}

// Handle runs the IMAP state machine until the client disconnects or LOGOUTs.
// SetIdlePollInterval sets how often an idling session re-reads its selected mailbox to
// report what other sources changed: new messages, expunges and flag changes (RFC 2177;
// RFC 3501 §§6.4.6, 7). Zero keeps the default, 15 seconds. Call it before Handle.
func (s *Session) SetIdlePollInterval(d time.Duration) { s.idlePoll = d }

func (s *Session) Handle() {
	defer func() { _ = s.conn.Close() }()
	// "A server MUST NOT unilaterally close the connection without sending an untagged
	// BYE response that contains the reason" (RFC 3501 §3.4): a session the Server's
	// shutdown ends in the middle of a command still says why.
	defer func() {
		if s.stopping() && !s.saidBye {
			s.sendShutdownBye()
		}
	}()
	defer s.recoverPanic() // contain any panic from a backend method or handler

	slog.Info("imap: new connection", "remote", s.conn.RemoteAddr())

	// Send greeting. The CAPABILITY response code is built from the live session
	// state (via s.capabilities()) rather than hardcoded, so it reflects the TLS
	// state and the LOGINDISABLED policy and never advertises what the server
	// would refuse (RFC 3501 §7.1.1).
	s.send("* OK [CAPABILITY %s] %s IMAP4rev1 ready", s.capabilities(), s.hostname)

	for {
		// A Server that begins to stop interrupts the read below; checking again after
		// setting the deadline, which replaces the one that interrupted it, closes the
		// window between the two.
		if s.stopping() {
			return
		}
		_ = s.conn.SetDeadline(time.Now().Add(autologoutTimeout))
		if s.stopping() {
			return
		}

		line, tooLong, nonSyncLiteral, err := s.readCommandLine()
		if err != nil {
			if s.stopping() {
				return
			}
			if isAutologout(err) {
				// Inactivity autologout (RFC 3501 §5.4): announce the close with an
				// untagged BYE (§7.1.5) before the deferred Close drops the socket,
				// rather than disconnecting silently.
				slog.Info("imap: autologout on inactivity", "remote", s.conn.RemoteAddr())
				s.sendAutologoutBye()
				return
			}
			slog.Debug("imap: connection closed", "remote", s.conn.RemoteAddr(), "error", err)
			return
		}
		if tooLong {
			slog.Debug("imap: command line too long", "remote", s.conn.RemoteAddr(), "limit", MaxCommandLineLength)
			s.send("* BAD Command line too long (limit %d bytes)", MaxCommandLineLength)
			if nonSyncLiteral {
				// The discarded line may have ended in a LITERAL+ marker whose
				// octets are already in flight; reading on would run them as
				// commands. RFC 7888 §4 permits BYE-and-close here.
				s.send("* BYE Command line too long with a pending literal")
				return
			}
			continue
		}

		if line == "" {
			continue
		}

		slog.Debug("imap: recv", "remote", s.conn.RemoteAddr(), "cmd", line)

		// IMAP commands are: <tag> <command> [<args>]
		tag, cmd, args := wire.ParseCommand(line)
		if tag == "" {
			// No tag and command pair: the command cannot be determined, so
			// answer with an untagged BAD (RFC 3501 §7.1.3) instead of dropping
			// the line and leaving the client waiting for a completion.
			s.send("* BAD Missing tag or command")
			continue
		}
		if !wire.ValidTag(tag) {
			// A tag is ASTRING-CHARs other than "+" (RFC 3501 §9), so a "*" or
			// "+" tag cannot pass for an untagged or continuation response. The
			// tag is not echoed.
			s.send("* BAD Invalid tag")
			continue
		}

		// Protocol command names remain ASCII even with UTF8=ACCEPT. Unicode
		// case folding must not turn an unknown name into SELECT or STORE.
		if !wire.IsASCII(cmd) {
			s.tagged(tag, "BAD", "Invalid command name")
			_, _, sync, literal := wire.SplitTrailingLiteral(args)
			if literal && !sync {
				s.send("* BYE Invalid command with a pending literal")
				return
			}
			continue
		}
		ucmd := strings.ToUpper(cmd)

		// A synchronizing literal {n} (or LITERAL+ {n+}) may stand in for any
		// string/astring argument of any command (RFC 3501 §4.3): send the "+"
		// continuation, read the octets, and splice them into the argument text so
		// the per-command parsers below see a fully materialized command line —
		// rather than misreading the octet line as a new command and desyncing.
		// APPEND is the sole exception: its terminal message literal is read on
		// handleAppend's large-message path, which needs the raw octets, not a
		// re-encoded argument; handleAppend reads a literal mailbox name itself.
		if ucmd != "APPEND" {
			materialized, argsOK, closeConn := s.readLiteralArgs(tag, args)
			if closeConn {
				return // buffered unread octets must never become new commands
			}
			if !argsOK {
				// A refused SELECT or EXAMINE is still a failed selection
				// attempt, so it deselects as handleSelect would.
				if (ucmd == "SELECT" || ucmd == "EXAMINE") && s.auth.authenticated {
					s.deselect()
				}
				continue // safe refusal before a continuation or after a complete command
			}
			args = materialized
		}

		// These commands take no arguments, and SP is exactly one space that only
		// separates (RFC 3501 §9), so anything after the command name, even a
		// lone trailing space, is refused.
		if wire.NoArguments[ucmd] && len(line) > len(tag)+1+len(cmd) {
			s.tagged(tag, "BAD", ucmd+" takes no arguments")
			continue
		}

		switch ucmd {
		case "CAPABILITY":
			s.handleCapability(tag)
		case "STARTTLS":
			if s.handleSTARTTLS(tag) {
				return
			}
		case "LOGIN":
			s.handleLogin(tag, args)
		case "AUTHENTICATE":
			s.handleAuthenticate(tag, args)
		case "LIST":
			s.handleList(tag, args, "LIST")
		case "LSUB":
			// Every existing folder counts as subscribed, so LSUB lists the same
			// names, but each reply is an untagged LSUB (RFC 3501 §7.2.3).
			s.handleList(tag, args, "LSUB")
		case "SUBSCRIBE":
			s.handleSubscribe(tag, args)
		case "UNSUBSCRIBE":
			s.handleUnsubscribe(tag, args)
		case "SELECT":
			s.handleSelect(tag, args, false)
		case "EXAMINE":
			s.handleSelect(tag, args, true) // read-only select (RFC 3501 §6.3.2)
		case "STATUS":
			s.handleStatus(tag, args)
		case "FETCH":
			s.handleFetch(tag, args)
		case "SEARCH":
			s.handleSearch(tag, args)
		case "STORE":
			s.handleStore(tag, args)
		case "COPY":
			s.handleCopy(tag, args)
		case "MOVE":
			s.handleMove(tag, args)
		case "CREATE":
			s.handleCreate(tag, args)
		case "DELETE":
			s.handleDelete(tag, args)
		case "RENAME":
			s.handleRename(tag, args)
		case "APPEND":
			if s.handleAppend(tag, args) {
				return
			}
		case "NOOP":
			// NOOP is the preferred way to poll for new messages and status
			// updates (RFC 3501 §6.1.2): report what other sources changed.
			s.pollSelected()
			s.tagged(tag, "OK", "NOOP completed")
		case "CHECK":
			// CHECK is a Selected State command (RFC 3501 §6.4.1): reject it when
			// no mailbox is selected — which also covers the non-authenticated
			// state, since a mailbox can only be selected after authentication.
			if s.selected == nil {
				s.tagged(tag, "BAD", "CHECK valid only in selected state")
				break
			}
			// With no housekeeping to do, "CHECK is equivalent to NOOP" (RFC
			// 3501 §6.4.1).
			s.pollSelected()
			s.tagged(tag, "OK", "CHECK completed")
		case "CLOSE":
			// CLOSE is a Selected State command (RFC 3501 §6.4.2): reject it when
			// no mailbox is selected rather than answering OK.
			if s.selected == nil {
				s.tagged(tag, "BAD", "CLOSE valid only in selected state")
				break
			}
			// Implicitly expunge \Deleted messages (RFC 3501 §6.4.2), but never
			// for a mailbox opened read-only via EXAMINE (§6.3.2).
			if !s.readOnly && !s.closeExpunge(tag) {
				break
			}
			s.selected = nil
			s.readOnly = false
			s.messages = nil
			s.saved = nil
			s.deleted = make(map[uint32]bool)
			s.tagged(tag, "OK", "CLOSE completed")
		case "EXPUNGE":
			s.handleExpunge(tag)
		case "UNSELECT":
			s.handleUnselect(tag)
		case "ENABLE":
			s.handleEnable(tag, args)
		case "NAMESPACE":
			// One personal namespace: folders sit at the top, under "/" as LIST
			// gives it; there are no other users' or shared ones (RFC 2342 §5).
			if !s.auth.authenticated {
				s.tagged(tag, "NO", "Not authenticated")
				break
			}
			s.send(`* NAMESPACE (("" "/")) NIL NIL`)
			s.tagged(tag, "OK", "NAMESPACE completed")
		case "GETQUOTA":
			s.handleGetQuota(tag, args)
		case "GETQUOTAROOT":
			s.handleGetQuotaRoot(tag, args)
		case "IDLE":
			if s.handleIdle(tag) {
				return
			}
		case "UID":
			s.handleUID(tag, args)
		case "LOGOUT":
			s.send("* BYE IMAP4rev1 Server logging out")
			s.tagged(tag, "OK", "LOGOUT completed")
			return
		default:
			s.tagged(tag, "BAD", "Unknown command")
		}
	}
}

// ── SEARCH ────────────────────────────────────────────────────────────

// ── UID command ───────────────────────────────────────────────────────

func (s *Session) handleUID(tag, args string) {
	if !s.auth.authenticated {
		s.tagged(tag, "NO", "Not authenticated")
		return
	}
	if s.selected == nil {
		s.tagged(tag, "NO", "No mailbox selected")
		return
	}

	// Parse "UID FETCH 1:* (FLAGS)" → subCmd="FETCH", subArgs="1:* (FLAGS)"
	parts := strings.SplitN(args, " ", 2)
	if len(parts) < 1 {
		s.tagged(tag, "BAD", "UID requires a command")
		return
	}
	subCmd := strings.ToUpper(parts[0])
	subArgs := ""
	if len(parts) > 1 {
		subArgs = parts[1]
	}

	// Convert UID sequence set to message sequence numbers
	switch subCmd {
	case "FETCH":
		s.handleUIDFetch(tag, subArgs)
	case "STORE":
		s.handleUIDStore(tag, subArgs)
	case "COPY":
		s.handleUIDCopy(tag, subArgs)
	case "MOVE":
		s.handleUIDMove(tag, subArgs)
	case "SEARCH":
		s.handleUIDSearch(tag, subArgs)
	case "EXPUNGE":
		s.handleUIDExpunge(tag, subArgs)
	default:
		s.tagged(tag, "BAD", "Unknown UID command")
	}
}

// uidToSeq converts a UID to a sequence number (1-based) in the current message list.
func (s *Session) uidToSeq(uid uint32) int {
	for i, msg := range s.messages {
		if msg.UID == uid {
			return i + 1
		}
	}
	return 0
}

// parseUIDSet parses a UID set like "1,3:5,*" and returns matching sequence numbers.
func (s *Session) parseUIDSet(uidSetStr string) []int {
	if uidSetStr == "$" {
		return s.savedSeqNumbers()
	}
	var seqNums []int
	for _, part := range strings.Split(uidSetStr, ",") {
		part = strings.TrimSpace(part)
		if strings.Contains(part, ":") {
			rangeParts := strings.SplitN(part, ":", 2)
			var startUID, endUID uint32
			if rangeParts[0] == "*" {
				if len(s.messages) > 0 {
					startUID = s.messages[len(s.messages)-1].UID
				}
			} else {
				v, _ := strconv.ParseUint(rangeParts[0], 10, 32)
				startUID = uint32(v)
			}
			if rangeParts[1] == "*" {
				if len(s.messages) > 0 {
					endUID = s.messages[len(s.messages)-1].UID
				}
			} else {
				v, _ := strconv.ParseUint(rangeParts[1], 10, 32)
				endUID = uint32(v)
			}
			if startUID > endUID {
				startUID, endUID = endUID, startUID
			}
			for i, msg := range s.messages {
				if msg.UID >= startUID && msg.UID <= endUID {
					seqNums = append(seqNums, i+1)
				}
			}
		} else if part == "*" {
			if len(s.messages) > 0 {
				seqNums = append(seqNums, len(s.messages))
			}
		} else {
			uid, _ := strconv.ParseUint(part, 10, 32)
			seq := s.uidToSeq(uint32(uid))
			if seq > 0 {
				seqNums = append(seqNums, seq)
			}
		}
	}
	return seqNums
}

// ── Output helpers ────────────────────────────────────────────────────

func (s *Session) send(format string, args ...interface{}) {
	if strings.HasPrefix(format, "* BYE") {
		s.saidBye = true
	}
	s.writeString(fmt.Sprintf(format, args...) + "\r\n")
}

func (s *Session) tagged(tag, status, msg string) {
	s.writeString(tag + " " + status + " " + msg + "\r\n")
}

// refuseReadOnly answers a tagged NO and returns true when the current selection
// was opened read-only via EXAMINE (RFC 3501 §6.3.2): no command may change the
// mailbox's permanent state. State-mutating handlers (STORE, EXPUNGE, MOVE and
// their UID variants) call this before touching the backend.
func (s *Session) refuseReadOnly(tag string) bool {
	if s.readOnly {
		s.tagged(tag, "NO", "Mailbox is read-only")
		return true
	}
	return false
}

// writeString emits str to the client and flushes. Protocol writes are
// best-effort: a failure surfaces on the next read, so the error is ignored.
func (s *Session) writeString(str string) {
	_, _ = s.writer.WriteString(str)
	_ = s.writer.Flush()
}

// boolPtr returns a pointer to b, for building a FlagUpdate.
func boolPtr(b bool) *bool { return &b }
