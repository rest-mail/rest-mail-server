package imap

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/restmail/restmail/internal/gateway/apiclient"
)

// Session represents a single IMAP conversation with a client.
type Session struct {
	conn      net.Conn
	reader    *bufio.Reader
	writer    *bufio.Writer
	api       *apiclient.Client
	hostname  string
	tlsConfig *tls.Config

	// Session state
	tls_       bool
	auth       *authState
	selected   *selectedMailbox
	messages   []apiclient.MessageSummary // cached message list for current selection
	deleted    map[uint]bool              // message IDs flagged \Deleted in this session
}

type authState struct {
	authenticated bool
	email         string
	token         string
	accountID     uint
}

type selectedMailbox struct {
	name   string
	total  int64
	unread int64
}

// NewSession creates a new IMAP session.
func NewSession(conn net.Conn, api *apiclient.Client, hostname string, tlsConfig *tls.Config) *Session {
	return &Session{
		conn:      conn,
		reader:    bufio.NewReader(conn),
		writer:    bufio.NewWriter(conn),
		api:       api,
		hostname:  hostname,
		tlsConfig: tlsConfig,
		auth:      &authState{},
		deleted:   make(map[uint]bool),
	}
}

// Handle runs the IMAP state machine.
func (s *Session) Handle() {
	defer s.conn.Close()

	slog.Info("imap: new connection", "remote", s.conn.RemoteAddr())

	// Send greeting
	s.send("* OK [CAPABILITY IMAP4rev1 STARTTLS AUTH=PLAIN] %s IMAP4rev1 RestMail", s.hostname)

	for {
		s.conn.SetDeadline(time.Now().Add(30 * time.Minute))

		line, err := s.reader.ReadString('\n')
		if err != nil {
			slog.Debug("imap: connection closed", "remote", s.conn.RemoteAddr(), "error", err)
			return
		}

		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			continue
		}

		slog.Debug("imap: recv", "remote", s.conn.RemoteAddr(), "cmd", line)

		// IMAP commands are: <tag> <command> [<args>]
		tag, cmd, args := parseIMAPCommand(line)
		if tag == "" {
			continue
		}

		switch strings.ToUpper(cmd) {
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
			s.handleList(tag, args)
		case "LSUB":
			s.handleList(tag, args) // treat LSUB same as LIST
		case "SELECT":
			s.handleSelect(tag, args)
		case "EXAMINE":
			s.handleSelect(tag, args) // read-only select
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
		case "CREATE":
			s.handleCreate(tag, args)
		case "NOOP":
			s.tagged(tag, "OK", "NOOP completed")
		case "CHECK":
			s.tagged(tag, "OK", "CHECK completed")
		case "CLOSE":
			s.selected = nil
			s.messages = nil
			s.deleted = make(map[uint]bool)
			s.tagged(tag, "OK", "CLOSE completed")
		case "EXPUNGE":
			s.handleExpunge(tag)
		case "GETQUOTA":
			s.handleGetQuota(tag, args)
		case "GETQUOTAROOT":
			s.handleGetQuotaRoot(tag, args)
		case "IDLE":
			s.handleIdle(tag)
		case "LOGOUT":
			s.send("* BYE IMAP4rev1 Server logging out")
			s.tagged(tag, "OK", "LOGOUT completed")
			return
		default:
			s.tagged(tag, "BAD", "Unknown command")
		}
	}
}

func (s *Session) handleCapability(tag string) {
	caps := "IMAP4rev1 QUOTA"
	if !s.tls_ && s.tlsConfig != nil {
		caps += " STARTTLS"
	}
	if s.tls_ || s.tlsConfig == nil {
		caps += " AUTH=PLAIN"
	}
	s.send("* CAPABILITY %s", caps)
	s.tagged(tag, "OK", "CAPABILITY completed")
}

func (s *Session) handleSTARTTLS(tag string) bool {
	if s.tls_ {
		s.tagged(tag, "BAD", "Already in TLS mode")
		return false
	}
	if s.tlsConfig == nil {
		s.tagged(tag, "BAD", "TLS not available")
		return false
	}

	s.tagged(tag, "OK", "Begin TLS negotiation now")

	tlsConn := tls.Server(s.conn, s.tlsConfig)
	if err := tlsConn.Handshake(); err != nil {
		slog.Warn("imap: TLS handshake failed", "error", err)
		return true
	}

	s.conn = tlsConn
	s.reader = bufio.NewReader(tlsConn)
	s.writer = bufio.NewWriter(tlsConn)
	s.tls_ = true

	slog.Info("imap: TLS established", "remote", s.conn.RemoteAddr())
	return false
}

func (s *Session) handleLogin(tag, args string) {
	if !s.tls_ && s.tlsConfig != nil {
		s.tagged(tag, "NO", "[PRIVACYREQUIRED] STARTTLS required")
		return
	}

	// Parse: LOGIN <user> <password>
	parts := parseIMAPArgs(args)
	if len(parts) < 2 {
		s.tagged(tag, "BAD", "LOGIN requires username and password")
		return
	}
	username := unquote(parts[0])
	password := unquote(parts[1])

	resp, err := s.api.Login(username, password)
	if err != nil {
		slog.Warn("imap: auth failed",
			"remote", s.conn.RemoteAddr(),
			"user", username,
			"event", "imap_auth_failed",
		)
		s.tagged(tag, "NO", "[AUTHENTICATIONFAILED] Invalid credentials")
		return
	}

	s.auth.authenticated = true
	s.auth.email = username
	s.auth.token = resp.Data.AccessToken
	s.auth.accountID = resp.Data.User.ID

	slog.Info("imap: authenticated", "remote", s.conn.RemoteAddr(), "user", username)
	s.tagged(tag, "OK", "[CAPABILITY IMAP4rev1] LOGIN completed")
}

func (s *Session) handleAuthenticate(tag, args string) {
	// Simplified — only support PLAIN
	if !strings.EqualFold(strings.TrimSpace(args), "PLAIN") {
		s.tagged(tag, "NO", "Unsupported mechanism")
		return
	}
	s.send("+")

	line, err := s.reader.ReadString('\n')
	if err != nil {
		return
	}
	decoded, err := decodeBase64(strings.TrimRight(line, "\r\n"))
	if err != nil {
		s.tagged(tag, "NO", "Invalid base64")
		return
	}
	parts := strings.SplitN(string(decoded), "\x00", 3)
	if len(parts) != 3 {
		s.tagged(tag, "NO", "Invalid PLAIN data")
		return
	}

	resp, err := s.api.Login(parts[1], parts[2])
	if err != nil {
		s.tagged(tag, "NO", "[AUTHENTICATIONFAILED] Invalid credentials")
		return
	}

	s.auth.authenticated = true
	s.auth.email = parts[1]
	s.auth.token = resp.Data.AccessToken
	s.auth.accountID = resp.Data.User.ID
	s.tagged(tag, "OK", "AUTHENTICATE completed")
}

func (s *Session) handleList(tag, args string) {
	if !s.auth.authenticated {
		s.tagged(tag, "NO", "Not authenticated")
		return
	}

	resp, err := s.api.ListFolders(s.auth.token, s.auth.accountID)
	if err != nil {
		s.tagged(tag, "NO", "Failed to list folders")
		return
	}

	for _, f := range resp.Data {
		attrs := ""
		switch f.Name {
		case "INBOX":
			// no special attributes
		case "Sent":
			attrs = `\Sent`
		case "Drafts":
			attrs = `\Drafts`
		case "Trash":
			attrs = `\Trash`
		case "Junk":
			attrs = `\Junk`
		}
		s.send(`* LIST (%s) "/" "%s"`, attrs, f.Name)
	}
	s.tagged(tag, "OK", "LIST completed")
}

func (s *Session) handleSelect(tag, args string) {
	if !s.auth.authenticated {
		s.tagged(tag, "NO", "Not authenticated")
		return
	}

	folder := unquote(strings.TrimSpace(args))
	if folder == "" {
		folder = "INBOX"
	}

	// Fetch messages for this folder
	msgResp, err := s.api.ListMessages(s.auth.token, s.auth.accountID, folder)
	if err != nil {
		s.tagged(tag, "NO", "Failed to select folder")
		return
	}

	s.messages = msgResp.Data
	total := int64(len(s.messages))
	var unread int64
	var recent int64
	for _, m := range s.messages {
		if !m.IsRead {
			unread++
		}
		recent++ // simplified: all messages are "recent"
	}

	s.selected = &selectedMailbox{
		name:   folder,
		total:  total,
		unread: unread,
	}

	s.send("* %d EXISTS", total)
	s.send("* %d RECENT", recent)
	s.send("* OK [UNSEEN %d]", unread)
	s.send("* OK [UIDVALIDITY 1]")
	if total > 0 {
		s.send("* OK [UIDNEXT %d]", s.messages[0].ID+1)
	} else {
		s.send("* OK [UIDNEXT 1]")
	}
	s.send("* FLAGS (\\Seen \\Answered \\Flagged \\Deleted \\Draft)")
	s.send("* OK [PERMANENTFLAGS (\\Seen \\Answered \\Flagged \\Deleted \\Draft \\*)]")

	s.tagged(tag, "OK", "[READ-WRITE] SELECT completed")
}

func (s *Session) handleStatus(tag, args string) {
	if !s.auth.authenticated {
		s.tagged(tag, "NO", "Not authenticated")
		return
	}

	// Parse: STATUS <mailbox> (MESSAGES UNSEEN RECENT)
	parts := parseIMAPArgs(args)
	if len(parts) < 1 {
		s.tagged(tag, "BAD", "STATUS requires mailbox name")
		return
	}
	folder := unquote(parts[0])

	msgResp, err := s.api.ListMessages(s.auth.token, s.auth.accountID, folder)
	if err != nil {
		s.tagged(tag, "NO", "Failed to get status")
		return
	}

	total := len(msgResp.Data)
	var unseen int
	for _, m := range msgResp.Data {
		if !m.IsRead {
			unseen++
		}
	}

	s.send(`* STATUS "%s" (MESSAGES %d RECENT %d UNSEEN %d)`, folder, total, total, unseen)
	s.tagged(tag, "OK", "STATUS completed")
}

func (s *Session) handleFetch(tag, args string) {
	if s.selected == nil {
		s.tagged(tag, "NO", "No mailbox selected")
		return
	}

	// Parse sequence set and data items
	// Simplified: handle "FETCH <n> (FLAGS)" and "FETCH <n> (BODY[])" etc.
	parts := strings.SplitN(args, " ", 2)
	if len(parts) < 2 {
		s.tagged(tag, "BAD", "FETCH requires sequence and data items")
		return
	}

	seqStr := parts[0]
	dataItems := strings.ToUpper(parts[1])

	// Parse sequence numbers
	seqNums := parseSequenceSet(seqStr, len(s.messages))

	for _, seq := range seqNums {
		if seq < 1 || seq > len(s.messages) {
			continue
		}
		msg := s.messages[seq-1]

		if strings.Contains(dataItems, "BODY[]") || strings.Contains(dataItems, "BODY.PEEK[]") || strings.Contains(dataItems, "RFC822") {
			// Full message fetch — get from API
			detail, err := s.api.GetMessage(s.auth.token, msg.ID)
			if err != nil {
				continue
			}

			// Build a simplified RFC 2822 message
			raw := buildRawMessage(detail.Data)
			flags := buildFlags(msg)

			s.send("* %d FETCH (FLAGS (%s) RFC822.SIZE %d BODY[] {%d}", seq, flags, len(raw), len(raw))
			fmt.Fprintf(s.writer, "%s)\r\n", raw)
			s.writer.Flush()

			// Auto-mark as read
			if !msg.IsRead {
				s.api.UpdateMessage(s.auth.token, msg.ID, map[string]interface{}{"is_read": true})
				s.messages[seq-1].IsRead = true
			}
		} else if strings.Contains(dataItems, "FLAGS") || strings.Contains(dataItems, "ENVELOPE") || strings.Contains(dataItems, "INTERNALDATE") {
			flags := buildFlags(msg)
			date := msg.ReceivedAt.Format("02-Jan-2006 15:04:05 -0700")
			envelope := buildEnvelope(msg)
			s.send("* %d FETCH (FLAGS (%s) INTERNALDATE \"%s\" RFC822.SIZE %d ENVELOPE %s UID %d)",
				seq, flags, date, msg.SizeBytes, envelope, msg.ID)
		} else {
			flags := buildFlags(msg)
			s.send("* %d FETCH (FLAGS (%s) UID %d)", seq, flags, msg.ID)
		}
	}

	s.tagged(tag, "OK", "FETCH completed")
}

// ── SEARCH ────────────────────────────────────────────────────────────

type searchCriterion struct {
	kind  string          // "all", "seen", "unseen", "flagged", "unflagged", "deleted", "undeleted",
	                      // "from", "to", "subject", "since", "before", "on", "uid", "not", "or"
	value string          // for string/date/uid criteria
	date  time.Time       // parsed date for since/before/on
	sub   []searchCriterion // for NOT (1 element) or OR (2 elements)
}

func (s *Session) handleSearch(tag, args string) {
	if s.selected == nil {
		s.tagged(tag, "NO", "No mailbox selected")
		return
	}

	criteria := s.parseSearchCriteria(strings.TrimSpace(args))

	var seqNums []string
	for i, msg := range s.messages {
		if s.matchesCriteria(msg, criteria) {
			seqNums = append(seqNums, strconv.Itoa(i+1))
		}
	}

	if len(seqNums) > 0 {
		s.send("* SEARCH %s", strings.Join(seqNums, " "))
	} else {
		s.send("* SEARCH")
	}
	s.tagged(tag, "OK", "SEARCH completed")
}

// parseSearchCriteria tokenizes the IMAP SEARCH arguments and builds criteria.
func (s *Session) parseSearchCriteria(args string) []searchCriterion {
	tokens := tokenizeSearch(args)
	var criteria []searchCriterion
	idx := 0
	for idx < len(tokens) {
		c, newIdx := parseSingleCriterion(tokens, idx)
		criteria = append(criteria, c)
		idx = newIdx
	}
	return criteria
}

// tokenizeSearch splits the search arguments into tokens, respecting quoted strings.
func tokenizeSearch(args string) []string {
	var tokens []string
	i := 0
	for i < len(args) {
		// Skip whitespace
		for i < len(args) && args[i] == ' ' {
			i++
		}
		if i >= len(args) {
			break
		}
		if args[i] == '"' {
			// Quoted string — find closing quote
			j := i + 1
			for j < len(args) && args[j] != '"' {
				j++
			}
			if j < len(args) {
				j++ // include closing quote
			}
			tokens = append(tokens, args[i:j])
			i = j
		} else {
			// Unquoted token
			j := i
			for j < len(args) && args[j] != ' ' {
				j++
			}
			tokens = append(tokens, args[i:j])
			i = j
		}
	}
	return tokens
}

// parseSingleCriterion parses one criterion from the token list starting at idx.
func parseSingleCriterion(tokens []string, idx int) (searchCriterion, int) {
	if idx >= len(tokens) {
		return searchCriterion{kind: "all"}, idx + 1
	}

	keyword := strings.ToUpper(tokens[idx])

	switch keyword {
	case "ALL":
		return searchCriterion{kind: "all"}, idx + 1
	case "SEEN":
		return searchCriterion{kind: "seen"}, idx + 1
	case "UNSEEN":
		return searchCriterion{kind: "unseen"}, idx + 1
	case "FLAGGED":
		return searchCriterion{kind: "flagged"}, idx + 1
	case "UNFLAGGED":
		return searchCriterion{kind: "unflagged"}, idx + 1
	case "DELETED":
		return searchCriterion{kind: "deleted"}, idx + 1
	case "UNDELETED":
		return searchCriterion{kind: "undeleted"}, idx + 1
	case "FROM":
		if idx+1 < len(tokens) {
			return searchCriterion{kind: "from", value: unquote(tokens[idx+1])}, idx + 2
		}
		return searchCriterion{kind: "all"}, idx + 1
	case "TO":
		if idx+1 < len(tokens) {
			return searchCriterion{kind: "to", value: unquote(tokens[idx+1])}, idx + 2
		}
		return searchCriterion{kind: "all"}, idx + 1
	case "SUBJECT":
		if idx+1 < len(tokens) {
			return searchCriterion{kind: "subject", value: unquote(tokens[idx+1])}, idx + 2
		}
		return searchCriterion{kind: "all"}, idx + 1
	case "SINCE":
		if idx+1 < len(tokens) {
			d := parseSearchDate(unquote(tokens[idx+1]))
			return searchCriterion{kind: "since", value: tokens[idx+1], date: d}, idx + 2
		}
		return searchCriterion{kind: "all"}, idx + 1
	case "BEFORE":
		if idx+1 < len(tokens) {
			d := parseSearchDate(unquote(tokens[idx+1]))
			return searchCriterion{kind: "before", value: tokens[idx+1], date: d}, idx + 2
		}
		return searchCriterion{kind: "all"}, idx + 1
	case "ON":
		if idx+1 < len(tokens) {
			d := parseSearchDate(unquote(tokens[idx+1]))
			return searchCriterion{kind: "on", value: tokens[idx+1], date: d}, idx + 2
		}
		return searchCriterion{kind: "all"}, idx + 1
	case "UID":
		if idx+1 < len(tokens) {
			return searchCriterion{kind: "uid", value: tokens[idx+1]}, idx + 2
		}
		return searchCriterion{kind: "all"}, idx + 1
	case "NOT":
		if idx+1 < len(tokens) {
			sub, newIdx := parseSingleCriterion(tokens, idx+1)
			return searchCriterion{kind: "not", sub: []searchCriterion{sub}}, newIdx
		}
		return searchCriterion{kind: "all"}, idx + 1
	case "OR":
		if idx+2 < len(tokens) {
			sub1, newIdx1 := parseSingleCriterion(tokens, idx+1)
			sub2, newIdx2 := parseSingleCriterion(tokens, newIdx1)
			return searchCriterion{kind: "or", sub: []searchCriterion{sub1, sub2}}, newIdx2
		}
		return searchCriterion{kind: "all"}, idx + 1
	default:
		// Unknown token — treat as ALL (ignore)
		return searchCriterion{kind: "all"}, idx + 1
	}
}

// parseSearchDate parses IMAP date formats: "1-Jan-2006" or "01-Jan-2006".
func parseSearchDate(s string) time.Time {
	s = strings.TrimSpace(s)
	// Try both single-digit and double-digit day formats
	for _, layout := range []string{"2-Jan-2006", "02-Jan-2006"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

func (s *Session) matchesCriteria(msg apiclient.MessageSummary, criteria []searchCriterion) bool {
	for _, c := range criteria {
		if !s.matchOne(msg, c) {
			return false
		}
	}
	return true
}

func (s *Session) matchOne(msg apiclient.MessageSummary, c searchCriterion) bool {
	switch c.kind {
	case "all":
		return true
	case "seen":
		return msg.IsRead
	case "unseen":
		return !msg.IsRead
	case "flagged":
		return msg.IsFlagged
	case "unflagged":
		return !msg.IsFlagged
	case "deleted":
		return s.deleted[msg.ID]
	case "undeleted":
		return !s.deleted[msg.ID]
	case "from":
		return strings.Contains(strings.ToLower(msg.Sender), strings.ToLower(c.value))
	case "to":
		// Check RecipientsTo (JSON array of strings)
		return strings.Contains(strings.ToLower(string(msg.RecipientsTo)), strings.ToLower(c.value))
	case "subject":
		return strings.Contains(strings.ToLower(msg.Subject), strings.ToLower(c.value))
	case "since":
		return !msg.ReceivedAt.Before(c.date)
	case "before":
		return msg.ReceivedAt.Before(c.date)
	case "on":
		y1, m1, d1 := msg.ReceivedAt.Date()
		y2, m2, d2 := c.date.Date()
		return y1 == y2 && m1 == m2 && d1 == d2
	case "uid":
		// Parse UID set and check if msg.ID is in it
		// We need a max UID — use a large number since UIDs are DB IDs
		uidSet := parseSequenceSet(c.value, int(msg.ID)+1000000)
		for _, uid := range uidSet {
			if uint(uid) == msg.ID {
				return true
			}
		}
		return false
	case "not":
		if len(c.sub) > 0 {
			return !s.matchOne(msg, c.sub[0])
		}
		return true
	case "or":
		if len(c.sub) >= 2 {
			return s.matchOne(msg, c.sub[0]) || s.matchOne(msg, c.sub[1])
		}
		return true
	default:
		return true // unknown criteria: don't filter
	}
}

func (s *Session) handleStore(tag, args string) {
	if s.selected == nil {
		s.tagged(tag, "NO", "No mailbox selected")
		return
	}

	// Parse: STORE <seq> +FLAGS (\Seen) or -FLAGS (\Seen)
	parts := strings.SplitN(args, " ", 3)
	if len(parts) < 3 {
		s.tagged(tag, "BAD", "STORE requires sequence, action, and flags")
		return
	}

	seqStr := parts[0]
	action := strings.ToUpper(parts[1])
	flagStr := parts[2]

	seqNums := parseSequenceSet(seqStr, len(s.messages))
	flags := parseFlags(flagStr)

	for _, seq := range seqNums {
		if seq < 1 || seq > len(s.messages) {
			continue
		}
		msg := &s.messages[seq-1]

		updates := map[string]interface{}{}

		for _, flag := range flags {
			switch flag {
			case `\Seen`:
				val := strings.HasPrefix(action, "+")
				updates["is_read"] = val
				msg.IsRead = val
			case `\Flagged`:
				val := strings.HasPrefix(action, "+")
				updates["is_flagged"] = val
				msg.IsFlagged = val
			case `\Deleted`:
				if strings.HasPrefix(action, "+") {
					if s.deleted == nil {
						s.deleted = make(map[uint]bool)
					}
					s.deleted[msg.ID] = true
				} else {
					delete(s.deleted, msg.ID)
				}
			}
		}

		if len(updates) > 0 {
			s.api.UpdateMessage(s.auth.token, msg.ID, updates)
		}

		newFlags := buildFlags(*msg)
		s.send("* %d FETCH (FLAGS (%s))", seq, newFlags)
	}

	s.tagged(tag, "OK", "STORE completed")
}

func (s *Session) handleCopy(tag, args string) {
	if s.selected == nil {
		s.tagged(tag, "NO", "No mailbox selected")
		return
	}

	parts := strings.SplitN(args, " ", 2)
	if len(parts) < 2 {
		s.tagged(tag, "BAD", "COPY requires sequence and destination")
		return
	}

	seqStr := parts[0]
	dest := unquote(strings.TrimSpace(parts[1]))

	seqNums := parseSequenceSet(seqStr, len(s.messages))

	for _, seq := range seqNums {
		if seq < 1 || seq > len(s.messages) {
			continue
		}
		msg := s.messages[seq-1]
		s.api.UpdateMessage(s.auth.token, msg.ID, map[string]interface{}{"folder": dest})
	}

	s.tagged(tag, "OK", "COPY completed")
}

func (s *Session) handleExpunge(tag string) {
	if s.selected == nil {
		s.tagged(tag, "NO", "No mailbox selected")
		return
	}

	// Process in reverse order so sequence numbers stay valid
	for i := len(s.messages) - 1; i >= 0; i-- {
		msg := s.messages[i]
		if !s.deleted[msg.ID] {
			continue
		}
		seq := i + 1
		// Delete via API
		if err := s.api.DeleteMessage(s.auth.token, msg.ID); err != nil {
			slog.Warn("imap: expunge failed", "msg_id", msg.ID, "error", err)
			continue
		}
		// Send untagged EXPUNGE response
		s.send("* %d EXPUNGE", seq)
		// Remove from messages slice
		s.messages = append(s.messages[:i], s.messages[i+1:]...)
	}

	// Update selected mailbox count
	if s.selected != nil {
		s.selected.total = int64(len(s.messages))
	}

	s.deleted = make(map[uint]bool)
	s.tagged(tag, "OK", "EXPUNGE completed")
}

func (s *Session) handleCreate(tag, args string) {
	if !s.auth.authenticated {
		s.tagged(tag, "NO", "Not authenticated")
		return
	}

	folder := unquote(strings.TrimSpace(args))
	if folder == "" {
		s.tagged(tag, "NO", "Missing folder name")
		return
	}
	// Reject folder names that are too long or contain path separators
	if len(folder) > 200 {
		s.tagged(tag, "NO", "Folder name too long")
		return
	}
	if strings.ContainsAny(folder, "\x00\r\n") {
		s.tagged(tag, "NO", "Invalid folder name")
		return
	}

	// Folders are implicit — they exist once a message is moved into them.
	// CREATE just validates and acknowledges.
	s.tagged(tag, "OK", "CREATE completed")
}

// handleGetQuota returns quota for a named quota root (RFC 2087).
func (s *Session) handleGetQuota(tag, args string) {
	if !s.auth.authenticated {
		s.tagged(tag, "NO", "Not authenticated")
		return
	}

	quota, err := s.api.GetQuota(s.auth.token, s.auth.accountID)
	if err != nil {
		s.tagged(tag, "NO", "Failed to get quota")
		return
	}

	// Report in KB (IMAP QUOTA uses 1024-byte units)
	used := quota.Data.QuotaUsedBytes / 1024
	limit := quota.Data.QuotaBytes / 1024
	s.send("* QUOTA \"\" (STORAGE %d %d)", used, limit)
	s.tagged(tag, "OK", "GETQUOTA completed")
}

// handleGetQuotaRoot returns the quota root for a mailbox (RFC 2087).
func (s *Session) handleGetQuotaRoot(tag, args string) {
	if !s.auth.authenticated {
		s.tagged(tag, "NO", "Not authenticated")
		return
	}

	mailbox := strings.Trim(args, "\" ")
	if mailbox == "" {
		s.tagged(tag, "BAD", "Missing mailbox name")
		return
	}

	quota, err := s.api.GetQuota(s.auth.token, s.auth.accountID)
	if err != nil {
		s.tagged(tag, "NO", "Failed to get quota")
		return
	}

	used := quota.Data.QuotaUsedBytes / 1024
	limit := quota.Data.QuotaBytes / 1024
	s.send("* QUOTAROOT %s \"\"", mailbox)
	s.send("* QUOTA \"\" (STORAGE %d %d)", used, limit)
	s.tagged(tag, "OK", "GETQUOTAROOT completed")
}

func (s *Session) handleIdle(tag string) {
	if !s.auth.authenticated {
		s.tagged(tag, "NO", "Not authenticated")
		return
	}

	s.send("+ idling")

	// Wait for DONE or timeout
	s.conn.SetDeadline(time.Now().Add(29 * time.Minute)) // RFC recommends <30 min
	for {
		line, err := s.reader.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		if strings.ToUpper(line) == "DONE" {
			break
		}
	}

	s.tagged(tag, "OK", "IDLE terminated")
}

// ── Output helpers ────────────────────────────────────────────────────

func (s *Session) send(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintf(s.writer, "%s\r\n", msg)
	s.writer.Flush()
}

func (s *Session) tagged(tag, status, msg string) {
	fmt.Fprintf(s.writer, "%s %s %s\r\n", tag, status, msg)
	s.writer.Flush()
}
