package apitypes

import "time"

// ImportMessageBody carries historical mail without passing it through delivery
// rules. RawMessage is base64 on the JSON wire, preserving arbitrary octets.
type ImportMessageBody struct {
	RawMessage   []byte    `json:"raw_message" description:"Base64-encoded original RFC 5322 message octets; never rewritten"`
	Folder       string    `json:"folder,omitempty" description:"Destination folder; omitted or empty means INBOX"`
	Flags        []string  `json:"flags,omitempty" description:"IMAP system flags: \\Seen, \\Answered, \\Flagged, \\Draft; unknown flags are refused"`
	InternalDate time.Time `json:"internal_date" format:"date-time" description:"Original IMAP INTERNALDATE, including its timezone"`
}

// ImportMessageResult identifies the stored copy. Imported=false means a copy
// with this Message-ID already existed in this account and folder; it is unchanged.
type ImportMessageResult struct {
	ID        uint   `json:"id"`
	Imported  bool   `json:"imported"`
	MessageID string `json:"message_id"`
	Folder    string `json:"folder"`
}
