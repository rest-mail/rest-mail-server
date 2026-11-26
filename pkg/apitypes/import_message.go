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

// OpenImportSessionBody names the importer for the summary. Both fields are optional.
type OpenImportSessionBody struct {
	ClientName    string `json:"client_name,omitempty" maxLength:"255" description:"The importing tool, as the summary will name it (for IMAP, the ID command's name field)"`
	ClientVersion string `json:"client_version,omitempty" maxLength:"255" description:"The importing tool's version (for IMAP, the ID command's version field)"`
}

// ImportSession is an open import: send its id as import_session with each message.
type ImportSession struct {
	ID                 string    `json:"id" description:"Send as the import_session query parameter of importMessage"`
	AccountID          uint      `json:"account_id"`
	ClientName         string    `json:"client_name,omitempty"`
	ClientVersion      string    `json:"client_version,omitempty"`
	StartedAt          time.Time `json:"started_at" format:"date-time"`
	IdleTimeoutSeconds int       `json:"idle_timeout_seconds" description:"A session with no import or close for this long is closed and its summary written"`
}

// ImportFolderCounts is one folder's part of an import summary.
type ImportFolderCounts struct {
	Imported     int64 `json:"imported" description:"Messages stored"`
	Deduplicated int64 `json:"deduplicated" description:"Messages already there with the same Message-ID and octets, skipped"`
	Refused      int64 `json:"refused" description:"Attempts refused or failed, storing nothing"`
	Bytes        int64 `json:"bytes" description:"Octets of the messages stored"`
	// MboxSeparatorStripped counts messages that began with an mbox "From " separator line,
	// which was dropped so the message starts at its first header (#1498).
	MboxSeparatorStripped int64 `json:"mbox_separator_stripped" description:"Messages imported or deduplicated whose leading mbox From_ separator line was dropped"`
}

// ImportSessionSummary is what a closed import did: counts per folder, never one row per message.
type ImportSessionSummary struct {
	ID                    string                        `json:"id"`
	AccountID             uint                          `json:"account_id"`
	ClientName            string                        `json:"client_name,omitempty"`
	ClientVersion         string                        `json:"client_version,omitempty"`
	StartedAt             time.Time                     `json:"started_at" format:"date-time"`
	EndedAt               time.Time                     `json:"ended_at" format:"date-time"`
	Imported              int64                         `json:"imported"`
	Deduplicated          int64                         `json:"deduplicated"`
	Refused               int64                         `json:"refused"`
	Bytes                 int64                         `json:"bytes"`
	MboxSeparatorStripped int64                         `json:"mbox_separator_stripped"`
	Folders               map[string]ImportFolderCounts `json:"folders" description:"Counts by destination folder; refusals naming an invalid folder are counted under (invalid)"`
}
