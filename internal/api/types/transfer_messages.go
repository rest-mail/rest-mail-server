package apitypes

// TransferMessagesBody names a set of messages in one folder of an account and
// the folder to copy or move them to, all in one transaction.
type TransferMessagesBody struct {
	SourceFolder string `json:"source_folder" minLength:"1" message:"required: the folder that holds the messages" missing:"required: the folder that holds the messages" description:"The folder that holds every message named; INBOX in any case is INBOX"`
	MessageIDs   []uint `json:"message_ids" minItems:"1" maxItems:"1000" missing:"required: 1 to 1000 message IDs" description:"The messages to copy or move, 1 to 1000 distinct IDs, in the order the result reports them"`
	Folder       string `json:"folder" minLength:"1" message:"required: the destination folder" missing:"required: the destination folder" description:"The destination folder; INBOX in any case is INBOX"`
	Move         bool   `json:"move,omitempty" description:"true moves the messages, removing them from source_folder; false or omitted copies them"`
}

// TransferMessagesResult is the ID each message got in the destination, in
// the order of the request's message_ids. A copy or a move always makes a new
// message with a new, higher ID, so an IMAP client sees a new UID (RFC 3501
// §2.3.1.1).
type TransferMessagesResult struct {
	IDs []uint `json:"ids" description:"The new message IDs in folder, one per message_ids entry, in the same order"`
}
