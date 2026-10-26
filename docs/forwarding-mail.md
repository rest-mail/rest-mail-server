# Forwarding messages and attachments

Forwarding uses the source message the caller may read and a sender account the
caller may send as. The ordinary recipient validation, sender-domain policy,
rate limit and outbound pipeline apply. The note appears above the original
message header and body in both the text and HTML alternatives.

The server re-attaches stored parts instead of wrapping the original as an
attached message. Binary files, empty attachments and inline Content-ID parts
are retained. The Sent copy has attachment rows and filesystem files or S3
objects of its own. Deleting an equal original object does not remove the copy.

The complete signed MIME message must fit `SMTP_MAX_MESSAGE_SIZE`, including
base64 encoding, MIME headers and boundaries. The sender's quota counts the
forwarded body and copied attachment bytes. Oversized forwards receive 413;
insufficient sender quota receives 422 with `mailbox_full`.

A missing, changed or unreadable source object fails the forward. The server
does not silently send a message with fewer attachments. Sent, attachment
references, quota and recipient queue or local-delivery rows commit together.
Notifications and queued traces are published after commit.

Object storage is outside the database transaction. A failed transaction can
leave unreferenced copied objects, while database references and quota changes
roll back. The source message and its attachment objects are not modified.
