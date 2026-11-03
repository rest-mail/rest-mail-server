# Import flag compatibility patch

This is go-imap v0.13.2, retained with its original tests and license, plus the
minimal interfaces and protocol changes needed to preserve imported keywords
and seed the engine's existing Deleted set from stored messages. A local module
replacement makes the server build reproducibly until these changes can move
upstream. No installed module cache is modified.

Import permits at most 100 distinct case-insensitive keywords per message,
255 ASCII bytes each, and five system flags. Keywords use RFC 3501 atom syntax
and cannot contain a backslash. FLAGS reports existing mailbox keywords; PERMANENTFLAGS also advertises `\*`.
Keyword STORE is included only because imapsync filters out keywords before
APPEND unless this wildcard is advertised, and RFC 3501 requires the advertised
capability to work. The same syntax, count and length bounds apply to STORE.
