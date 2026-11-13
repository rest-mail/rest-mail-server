package handlers

// messageListFields is what a route answering many messages reads: every column of a
// message but its stored source, of which it takes only the header section. messageOf
// works out a message's class and authentication results from those headers and reads
// nothing else of the source, while a source holds the whole message, attachments
// included, so a page of 100 could bring 100 times the largest message into memory
// (#1437). In the second real load run the messages of one folder read this way were
// nearly all of the 1.81 GB the database sent the API (#1583).
//
// A column added to the model has to be added here too;
// TestMessageListFieldsNameEveryColumn fails until it is.
const messageListFields = messageListColumns + `, ` + messageSourceHeaderSection + `, ` + messageDueColumns + `, ` + messageSnoozeColumn

const messageListColumns = `messages.id, messages.mailbox_id, messages.folder, messages.message_id,
	messages.in_reply_to, messages."references", messages.thread_id, messages.sender, messages.sender_name,
	messages.recipients_to, messages.recipients_cc, messages.subject, messages.body_text, messages.body_html,
	messages.headers, messages.auth_results, messages.sealed_hash, messages.rule_class, messages.calendar_events,
	messages.size_bytes, messages.raw_size, messages.downgraded_size, messages.imap_size, messages.imap_utf8_size, messages.received_tls, messages.require_tls, messages.submitted_at,
	messages.tls_version, messages.has_attachments, messages.is_answered, messages.is_read, messages.is_dsn,
	messages.is_flagged, messages.is_starred, messages.keywords, messages.imap_deleted, messages.mod_seq, messages.is_draft,
	messages.is_deleted, messages.received_at, messages.date_header, messages.created_at, messages.updated_at`

// messageSourceHeaderSection reads a source up to the first blank line, ended by LF or
// CRLF, whichever comes first, in each of the two columns a source can be in. A source
// with no blank line is all headers, and is read whole.
const messageSourceHeaderSection = `COALESCE(substr(messages.raw_message, 1, LEAST(
		NULLIF(position(E'\n\n' IN messages.raw_message), 0),
		NULLIF(position(E'\n\r\n' IN messages.raw_message), 0)) + 2), messages.raw_message) AS raw_message,
	COALESCE(substring(messages.raw_octets FROM 1 FOR LEAST(
		NULLIF(position('\x0a0a'::bytea IN messages.raw_octets), 0),
		NULLIF(position('\x0a0d0a'::bytea IN messages.raw_octets), 0)) + 2), messages.raw_octets) AS raw_octets`
