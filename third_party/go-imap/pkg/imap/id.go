package imap

import "strings"

// IDField is one field-value pair a client sent with the ID command ([RFC 2971]
// §3.1). Nil is true when the client sent NIL for the value, meaning it "can not
// or will not specify this information"; Value is then empty.
//
// [RFC 2971]: https://www.rfc-editor.org/rfc/rfc2971
type IDField struct {
	Name  string
	Value string
	Nil   bool
}

// ClientID is the identification a client sent with ID, in the order sent. The
// server has already held it to RFC 2971 §3.3's bounds: at most 30 pairs, field
// names at most 30 octets, values at most 1024 octets, and no field twice.
//
// RFC 2971 §3 says implementations "MUST NOT make operational changes based on
// the data sent as part of the ID command": it is for logs and audit records,
// never for deciding how to serve the client.
type ClientID []IDField

// Get returns the value of the named field and whether the client sent it with
// a value other than NIL. Field names are not case-sensitive (RFC 2971 §3.3).
func (id ClientID) Get(name string) (string, bool) {
	for _, f := range id {
		if strings.EqualFold(f.Name, name) {
			return f.Value, !f.Nil
		}
	}
	return "", false
}

// IDMailbox is the optional interface a [Mailbox] may also implement to learn
// what the client said it is with the ID command ([RFC 2971]). The server
// advertises ID and answers it in every state whether or not the Mailbox
// implements this; with it, the session hands the client's fields to the
// Mailbox once it is authenticated, or when ID arrives later in the session.
//
// SetClientID is called with the fields of the latest ID the client sent with a
// non-empty list. ID NIL, or an empty list, sends no fields and does not call it.
//
// [RFC 2971]: https://www.rfc-editor.org/rfc/rfc2971
type IDMailbox interface {
	SetClientID(id ClientID)
}
