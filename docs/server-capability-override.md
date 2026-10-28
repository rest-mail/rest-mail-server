# Server capability override

`RESTMAIL_SERVER_CAPABILITIES` is an operator-controlled replacement for the stored server capability ceiling. Set the same value on the API and all gateway processes, then restart them. The value is read once at startup; changing the stored ceiling does not change a configured override.

The value is a comma-separated list of existing capability names, for example:

```sh
RESTMAIL_SERVER_CAPABILITIES=domains:read,domains:write
```

Each ordinary actor keeps only the intersection of its own grants and this list. Listing a capability does not grant it to an actor, and never widens the scope of an actor's grants. A global superadmin remains exempt for recovery.

- Unset: use the stored server ceiling, checked live on authorization.
- Set to an empty value: deny every capability to ordinary actors.
- Set to a list: ignore the stored server ceiling and use that list instead.

Whitespace around names is ignored. Unknown names, `*`, and empty list elements are startup errors. The list is validated against the existing capability vocabulary without writing the override to the database. A failed configuration does not replace a previously configured ceiling.

SMTP checks that load grants in the gateway process use this override. IMAP and POP3 authorize through the API; their startup validation does not replace the API's authorization. Configure every process consistently. Gateways need a database connection to validate a set override, even where their database connection is normally optional.

To return to the stored ceiling, unset the variable on every process and restart them. This change does not add mailbox-session lockdown, a ceiling editor, or protocol-session revocation; those are separate parts of issue #324.
