# Attachment storage

PostgreSQL holds message and attachment metadata, including mailbox ownership,
checksums and object references. The original MIME message remains in PostgreSQL
for IMAP/POP3 byte fidelity. Extracted attachments can use local files (the
default) or a private S3-compatible store such as Garage.

Set these API environment variables to use S3:

| Variable | Value |
| --- | --- |
| `ATTACHMENT_STORAGE_TYPE` | `s3` |
| `ATTACHMENT_S3_ENDPOINT` | Garage's HTTP(S) origin; omit for AWS |
| `ATTACHMENT_S3_REGION` | `garage` for Garage; default `us-east-1` |
| `ATTACHMENT_S3_BUCKET` | A dedicated private attachment bucket |
| `ATTACHMENT_S3_PREFIX` | Default `attachments/` |
| `ATTACHMENT_S3_FORCE_PATH_STYLE` | Default `true` |
| `ATTACHMENT_S3_ACCESS_KEY_ID` | A key scoped to this bucket |
| `ATTACHMENT_S3_SECRET_ACCESS_KEY` | The matching secret, supplied securely |

The key needs object read, write and delete, without bucket ownership or access
to other buckets. Store credentials in deployment secrets. With Helm, use
`api.attachments.storageType: s3` and the corresponding `api.attachments.s3`
values. Either supply credentials through a secret-aware deployment tool or set
`existingSecret` to a Secret containing `ACCESS_KEY_ID` and `SECRET_ACCESS_KEY`.
Custom private endpoints may need `s3.networkPolicy.to` and `port` set; the
default policy already permits public HTTP(S).

Pipelines use the server's configured store. They cannot select a different
endpoint, bucket or credentials. Object keys use the full SHA-256 content hash.
Downloads pass the existing mailbox authorization check and stream through the
API, including range requests. Objects and credentials are never sent to a
browser as public or presigned URLs.

## Uploading outgoing attachments

Create a draft, then upload each file with
`POST /api/v1/messages/draft/{id}/attachments`. Send its raw bytes as
`Content-Type: application/octet-stream`, with `X-Attachment-Filename` naming
the file and `X-Attachment-Content-Type` declaring its MIME type, for example
`application/pdf`. Filenames must be valid UTF-8 of 1–255 bytes without CR,
LF or NUL; they are display metadata, never object paths. Empty files are valid.

The 201 response contains the attachment's `id`, `message_id`, `filename`,
`content_type`, `size_bytes`, SHA-256 `checksum` and `created_at`. It never
contains storage credentials or object references. `GET` on the same path lists
the draft's attachments; `DELETE .../attachments/{attachmentId}` removes one
and returns 204. All three operations require `mail:write` on the draft.
Another account's draft or an attachment outside the named draft returns 404.

Uploads stream through a temporary file to the configured attachment store;
they do not use the JSON API's body-size allowance. `SMTP_MAX_MESSAGE_SIZE`
bounds the combined draft text and attachment bytes, with 413
`message_too_large` for excess bytes, including unknown-length uploads.
Account and owner storage quotas are reserved transactionally; insufficient
space returns 422 `mailbox_full`. Deleting an upload releases its charge.
The existing `MIME_MAX_PARTS` limit also bounds attachment count, including
the body parts, so repeated empty uploads cannot bypass structural bounds.

`POST /api/v1/messages/draft/{id}/send` reads and verifies the stored uploads,
builds MIME, and stores the same raw message in Sent and the outbound queue.
The final encoded MIME must also fit `SMTP_MAX_MESSAGE_SIZE`; base64 and MIME
headers can make it larger than the uploaded bytes. A refusal preserves the
draft and its uploads. A successful send atomically replaces their quota charge
with the Sent copy's charge and removes the draft's attachment rows.

Inline JSON `attachments` fields remain explicitly refused on plain send and
draft create/update; arbitrary attachment IDs are not accepted by plain send.
Use the draft upload flow instead. Removed and failed-upload objects with no
remaining database reference are reclaimed by the attachment cleaner after
its configured grace period, rather than being deleted while another message
may still reference the same content hash.

## Moving an existing installation

1. Back up PostgreSQL and the attachment files. Provision the private bucket
   and scoped key, then configure S3 **while keeping the existing attachment
   volume mounted**. New extracted attachments now go to S3; old files remain
   readable.
2. Run `restmail-api check-attachment-storage` in the API's environment. It
   verifies write, read, range read and deletion using a random temporary object.
   Its JSON output contains only the result.
3. Run `restmail-api migrate-attachments` with the same configuration and the
   old volume mounted at `/attachments`. Each file is streamed to S3 and read
   back to verify its size and checksum before its PostgreSQL reference changes.
   Original files stay intact. A failure leaves that row pointing to its source;
   rerunning resumes the migration. Concurrent arrivals are safe because new
   attachments already use S3.
4. Only after successful migration reports `filesystem_remaining: 0`, disable
   `api.attachments.persistence.enabled` and restart the API. Keep the old
   volume and backup through the rollback window. The Helm PVC has a retention
   annotation; this migration never deletes it or its files.

Back up the PostgreSQL metadata and the bucket together. Rolling software back
to a version without S3 support requires restoring the matching metadata and
files; old binaries cannot interpret S3 object references. The attachment
cleaner reclaims unreferenced filesystem and S3 objects after its grace period.

Run `python3 scripts/garage-attachment-test.py` on a Docker-capable test runner
to exercise the real pinned Garage image, including binary objects, range reads,
deletion and denial of another bucket. CI runs this as a required part of `CI`.
