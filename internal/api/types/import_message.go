package apitypes

import "time"

// ImportMessageBody carries historical mail without passing it through delivery
// rules. RawMessage is base64 on the JSON wire, preserving arbitrary octets.
type ImportMessageBody struct {
	RawMessage   []byte    `json:"raw_message" description:"Base64-encoded original RFC 5322 message octets; never rewritten"`
	Folder       string    `json:"folder,omitempty" description:"Destination folder; omitted or empty means INBOX"`
	Flags        []string  `json:"flags,omitempty" description:"IMAP flags: \\Seen, \\Answered, \\Flagged, \\Draft, \\Deleted and up to 100 case-insensitive keywords (1–255 ASCII bytes; RFC 3501 atoms without backslash); unknown system flags are refused"`
	InternalDate time.Time `json:"internal_date" format:"date-time" description:"Original IMAP INTERNALDATE, including its timezone"`
	// AlwaysStore is how an IMAP client's APPEND or COPY asks for what those
	// commands mean: a new message, whatever the folder holds already. A migration
	// leaves it off, so that a rerun skips what it has already stored.
	AlwaysStore bool `json:"always_store,omitempty" description:"Store a new message even if this folder already holds this Message-ID and these exact octets. Omitted or false skips such a message."`
	// MatchSubmission is how an IMAP client's APPEND asks not to store a second copy of
	// what the account has just sent: a mail app uploads its own copy of a message
	// submission has already saved in Sent (#1707). It is checked before AlwaysStore. A
	// migration leaves it off, so that nothing it brings is taken for a sent message.
	MatchSubmission bool `json:"match_submission,omitempty" description:"When folder is Sent, answer with the message authenticated SMTP submission saved in this account in the last 5 minutes under this Message-ID, instead of storing another. Omitted or false never does."`
}

// ImportMessageResult identifies the stored copy. Imported=false means a copy
// with this Message-ID and these exact octets already existed in this account and
// folder, or MatchSubmission found the copy submission saved; it is unchanged. A
// request with AlwaysStore answers Imported=true unless MatchSubmission found one.
type ImportMessageResult struct {
	ID        uint   `json:"id"`
	Imported  bool   `json:"imported"`
	MessageID string `json:"message_id"`
	Folder    string `json:"folder"`
}
