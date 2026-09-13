package pop3

import (
	"github.com/restmail/restmail/internal/gateway/apiclient"
	"github.com/restmail/restmail/internal/gateway/rawmsg"
)

// buildRawMessage renders a stored message as RFC 5322 bytes for RETR and TOP.
//
// The IMAP gateway hands a client the same stored message on FETCH, so the two
// must agree byte for byte: both delegate to rawmsg.Build rather than keeping
// their own copy of the construction rules.
func buildRawMessage(msg apiclient.MessageDetail) string {
	return rawmsg.Build(msg)
}
