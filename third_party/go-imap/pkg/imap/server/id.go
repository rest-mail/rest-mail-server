package server

import (
	"log/slog"

	"github.com/rest-mail/go-imap/pkg/imap"
	"github.com/rest-mail/go-imap/pkg/imap/internal/wire"
)

// SetServerID sets the name and version the session gives in its ID response
// (RFC 2971 §3.2). Nothing else is sent: no host, OS or contact details, which
// RFC 2971 §7 warns can help an attacker. An empty name leaves the response NIL,
// which §3.2 allows. Call it before Handle.
func (s *Session) SetServerID(name, version string) {
	s.serverID = nil
	if name == "" {
		return
	}
	s.serverID = []wire.IDPair{{Name: "name", Value: name}}
	if version != "" {
		s.serverID = append(s.serverID, wire.IDPair{Name: "version", Value: version})
	}
}

// SetServerID sets the name and version every session this server starts
// gives in its ID response (see [Session.SetServerID]). Call it before Serve.
func (s *Server) SetServerID(name, version string) {
	s.idName, s.idVersion = name, version
}

// handleID implements the ID command (RFC 2971), valid in any state (§3.1). The
// client's fields, held to §3.3's bounds, stay on the session and reach the
// Mailbox through [imap.IDMailbox]; nothing about how the session is served
// depends on them (§3). The answer is one untagged ID with the server's own
// name and version, then OK.
func (s *Session) handleID(tag, args string) {
	pairs, isNil, problem := wire.ParseIDParams(args)
	if problem != "" {
		s.tagged(tag, "BAD", problem)
		return
	}
	if !isNil && len(pairs) > 0 {
		id := make(imap.ClientID, len(pairs))
		for i, p := range pairs {
			id[i] = imap.IDField(p)
		}
		s.clientID = id
		name, _ := id.Get("name")
		version, _ := id.Get("version")
		// Only the program's name and version go in the log; the other fields
		// can describe the user's machine (RFC 2971 §7).
		slog.Info("imap: client id", "remote", s.conn.RemoteAddr(), "name", name, "version", version)
		s.deliverClientID()
	}
	s.send("* ID %s", wire.FormatIDParams(s.serverID))
	s.tagged(tag, "OK", "ID completed")
}

// deliverClientID hands the client's ID fields to an authenticated Mailbox
// that takes them. It runs when ID arrives after authentication and when a
// client that sent ID first then authenticates.
func (s *Session) deliverClientID() {
	if s.clientID == nil || s.mailbox == nil {
		return
	}
	if m, ok := s.mailbox.(imap.IDMailbox); ok {
		m.SetClientID(append(imap.ClientID(nil), s.clientID...))
	}
}
