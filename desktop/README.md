# REST Mail desktop

The Wails application reuses `../webmail/src` through Vite and TypeScript aliases.
Go owns authentication, the OS keychain and the relative API proxy. JavaScript
receives identity and setup state, never access or refresh tokens.

## Direct IMAP and SMTP accounts

Choose **IMAP and SMTP** from the desktop's existing Add account control, or
**Use IMAP and SMTP instead** on the sign-in screen. Go verifies both servers
before saving the account. IMAP supports implicit TLS (default 993) and mandatory
STARTTLS (default 143); SMTP supports mandatory STARTTLS (default 587) and
implicit TLS (default 465), with AUTH PLAIN or LOGIN only after verified TLS.
Certificate verification cannot be disabled. The native/MCP inputs additionally
accept custom ports with `imap_security` and `smtp_security` set to `tls` or
`starttls`. SMTP uses the IMAP username and password. OAuth and separate SMTP
credentials are not implemented.

Passwords live in the `REST Mail` OS keychain service under newline-free
`imap:<numeric-id>` keys. Non-secret account and folder-identity metadata lives
in the same keychain under `imap-accounts-v1`; no plaintext config file is used.
`Accounts.AddIMAPAccount`, `Accounts.ListAccounts`, and
`Accounts.RemoveIMAPAccount` expose no password in their results. The matching
MCP tools are `add_imap_account`, `list_accounts`, and `remove_imap_account`.
Mailbox MCP tools accept an optional `account_id` where an account is needed.
Sign out disconnects local accounts for the current run, retaining their saved
configuration. Re-adding the same settings verifies credentials and reconnects
without changing IDs; removal deletes the keychain password. Restart restores
saved accounts.

The composite Go transport merges local accounts with REST Mail accounts and
implements these REST-shaped routes without frontend account-type branches:

- `GET /api/v1/accounts`; `DELETE /api/v1/accounts/{id}` for local accounts.
- `GET/POST /api/v1/accounts/{id}/folders`, and `PATCH/DELETE` on a named folder.
- `GET /api/v1/accounts/{id}/folders/{folder}/messages`, with opaque paging.
- `GET /api/v1/accounts/{id}/search`, with folder-scoped or cross-folder paging.
- `GET/PATCH/DELETE /api/v1/messages/{id}`: MIME bodies and headers, read/star/
  flagged/answered/draft flags, moves, trash, and permanent deletion.
- `GET /api/v1/messages/{id}/attachments`, `/messages/{id}/raw`, and
  `/attachments/{id}` for attachment metadata and downloads.
- `POST /api/v1/messages/send` for plain-text/HTML messages, including Cc/Bcc.
- `GET /api/v1/accounts/{id}/events`, relaying INBOX changes through the existing
  SSE `new_message` and `folder_update` events. IDLE is preferred; servers without
  it are polled every 60 seconds. The notification includes sender and subject
  when the newest message is fetchable.

IDs reserve the JavaScript-safe integer range `[2^52, 2^53)` for desktop-local
objects. Account IDs are monotonically allocated `2^52 + sequence`, never reused.
Each `(account, folder, UIDVALIDITY)` receives a persisted 20-bit folder slot;
message IDs are `2^52 + (slot << 32) + UID`. This survives restarts and prevents
UID reuse from addressing a different message. Upstream account lists with IDs
in the reserved range fail closed instead of merging conflicting identities.
Attachment IDs hash the message ID and MIME part index into the same reserved
range, detect collisions, and are resolved after reading the message's attachment
list in the current process.

One serialized command connection is retained per account and reconnected before
operations as needed. Each open SSE subscription uses a separate INBOX watcher
connection, so IDLE does not block mailbox operations. Reads use BODY.PEEK; writes
are never automatically replayed. Safe move/delete requires MOVE or UIDPLUS as
appropriate; the client never uses a broad EXPUNGE that could delete somebody
else's marked messages. Message downloads are limited to 32 MiB. Lists are newest
UID first; cross-folder search walks folder names, then newest UIDs, with cursors
bound to account, query, folder and UIDVALIDITY.

This adapter does not implement server drafts, forwarding endpoints, attachment
sending, scheduled sends, calendar sends, receipt/seal verification, server-side
account settings, or saving an SMTP send into Sent. Unsupported local features
return an explicit error rather than being sent to the REST Mail server or
silently downgraded. INBOX is watched, not every folder; this is not an offline
store or a full synchronization engine.

## Build and run

Use Node 22, pnpm, Go and the pinned Wails CLI. Node 23 is unsupported by shared
webmail dependencies. Chore prefers Homebrew's `node@22` when installed; otherwise
put a supported Node version first on `PATH`.

From the repository root:

```sh
chore desktop:dev
chore desktop:build
```

Development runs with live reload. The macOS build is written to
`desktop/bin/RESTMail.app`; building does not install or release it. Sign in with
the server URL and account credentials. First-run accounts complete required
password changes and authenticator enrollment before mail opens.

For fixture-only UI development without a server or keychain:

```sh
DESKTOP_MOCK=1 chore desktop:dev
```

The mock is enabled only by Vite's development server and excluded from production
builds. It does not test native authentication or setup. Wails tasks install
frontend dependencies with pnpm.

## Desktop layout and typography

The title bar keeps REST Mail's brand beside the macOS window controls and provides
two layout choices: three columns, or a message list above the reader. The two
adjacent pane toggles show or hide folders and the reader. With the reader hidden,
opening a message uses a full-pane reader with **Back to messages**, keeping message
navigation separate from draft tabs. These controls also appear with checkmarks in
the native **View** menu: **⌘⌥1/2** select a layout, **⌘⌥S** toggles folders, and
**⌘⌥R** toggles the reader. Drag the message list's lower-right resize grip to
adjust its width in columns or its height in the stacked layout.

Layout, pane visibility, and separate split sizes are saved in localStorage per
server host and signed-in email. No account settings are changed. The desktop-only
theme defaults to Bricolage Grotesque and Instrument Sans, with an
11/12/13/15/22px type scale, 400/500/600 weights, and tabular counts and times.
Appearance settings can replace the palette, all four font roles, row spacing,
corner radius, and the theme's type scale. Shared webmail does not import these
styles, fonts, or preferences.

The default window is 1200×800, with a 1000×720 native content minimum (1000×754
including the macOS title-bar inset). Sign-in and setup
use a fixed brace composition measured from the approved sign-in capture: 70px
vertical overhang, 24px side gaps, and 160px-wide braces. The SVG path's inset
makes the visible overhang approximately 65px. Braces track the sheet's height,
not the window; resizing changes only the empty space outside the composition.
At least 32px of outer padding surrounds the composition. Taller setup or error
states scroll as one block without shrinking the braces. Sign-in uses a compact
48px logo and the same type scale as the mailbox.

## Import from Thunderbird

Choose **File → Import from Thunderbird…** (⇧⌘I on macOS), or open the
sidebar's **More tools → Import from Thunderbird…**. Select a detected profile,
the source accounts and folders to copy, and a destination REST Mail account.
Use the folder filter to find a specific folder in a large profile.
Direct IMAP accounts cannot be import destinations. Thunderbird should be quit
first; the sheet warns when it detects a running Thunderbird process. An IMAP
cache contains only messages Thunderbird has downloaded for offline use.

Profiles are discovered from `~/Library/Thunderbird/profiles.ini` on macOS,
`%APPDATA%/Thunderbird/profiles.ini` on Windows, and `~/.thunderbird/profiles.ini`
or `~/.config/thunderbird/profiles.ini` on Linux. Both relative and absolute
profile paths are supported. `Mail` and `ImapMail` are inspected read-only;
`.sbd` directories retain nesting and `.msf` indexes and symlinks are excluded.
Counts scan in the background with bounded memory, so a large profile remains
selectable while its message counts are being computed.

Inbox maps to `INBOX`; Sent, Drafts, Trash and Archives map to `Sent`, `Drafts`,
`Trash` and `Archive`. Nested folders keep their hierarchy. Mozilla read,
answered, flagged and IMAP-deleted bits become IMAP flags; expunged messages are
skipped. `From_` timestamps take precedence over the Date header. Mozilla mailbox
metadata is stripped after extraction so changing source flags does not change
the imported message octets. The shared streaming reader and API uploader live
in `../pkg/mailimport`; the console importer retains its original flags and
validation behavior through a compatibility wrapper.

Imports run one bounded message at a time (32 MiB maximum), in background jobs
with message and byte progress, cancellation and failure reasons. A malformed or
oversized message stops its source folder, with an explicit folder failure;
other selected folders continue. The source is never modified. Changed source
size or modification time is reported and requires a fresh discovery/restart.
The progress checkpoint is an atomic, private `import-progress.json` file under
the REST Mail configuration directory, with an instance suffix for test copies.
Reopening the sheet restores the previous folder selection and summary. Rerun
the same selection to resume: files are rescanned from the beginning, and the
server skips existing copies using Message-ID plus exact octets. This is not a
byte-offset resume and does not retry failures automatically. Server import
sessions record each run; changing the signed-in server stops uploads rather
than redirecting historical mail to a different server.

MCP tools use the same import service:

- `thunderbird_profiles`: discover profiles and folders; poll while `scanning`
  is true to receive complete counts. Account labels avoid exposing addresses.
- `thunderbird_import`: provide `profile_id`, `folders` (IDs returned by
  discovery), and numeric destination `account_id`; returns immediately.
- `import_status`: retrieve persisted progress and summary; pass `cancel: true`
  to cancel the active job. The job outlives the MCP request and sheet.

## Frontend validation

```sh
pnpm --dir desktop/frontend test
pnpm --dir desktop/frontend build
pnpm --dir webmail test
pnpm --dir webmail build
cd desktop && go test ./...
```

Regenerate TypeScript bindings after changing a registered Go service:

```sh
cd desktop && wails3 generate bindings -clean=true -ts -i
```

## Local mail delivery stack

On macOS with Docker Desktop, Go, and the existing desktop fixture database:

```sh
chore desktop:devstack:up
chore desktop:devstack:down
```

Sign the desktop into **http://localhost:18081** (use `localhost`, not an HTTP
IP literal: the desktop permits HTTP only for localhost). The API itself binds
only `127.0.0.1:18081`. The original API at `127.0.0.1:18080` stays running and
is never stopped or replaced. Both APIs retain the existing `pg-desktop-dev`
database on host port **55480**, never host port 5432.

The smallest complete delivery path needs an API, the SMTP gateway's queue
worker, and its HTTPS front, not the full reference-server topology. These three
containers have their own labelled Docker network. `desktop.test` resolves only
inside that network to the SMTP gateway and its HTTPS front, so ordinary queued
mail follows real SMTP discovery and verified RESTMAIL HTTPS delivery back into
the API. No host DNS or production delivery code is changed. Only the API's HTTP
port is published, on loopback; SMTP, HTTPS and the internal API stay private to
the network, and gateway metrics/submission listeners are disabled.

`up` builds the API and gateway from this checkout and generates certificates
with the repository's `go run ./cmd/instance mtls-issue` command. Delivery and
submission use separate client certificates, and the internal API requires mTLS.
The generated CA is trusted only by this development stack. The private network
is the explicit trusted peer for RESTMAIL deliveries. The script activates the
existing `desktop.test` domain with a development certificate if necessary and
disables **only its inbound greylist filter**, avoiding the default five-minute
delay between local test accounts; other pipeline filters remain intact. These
two fixture configuration changes persist after `down`.

The prerequisite fixture files under `/tmp/restmail-devserver` are reused, never
rewritten: `secrets.env` (JWT/master keys), `admin.pw`, `totp.secret`, and
`setup-user.pw`. No reseeding, password rotation, or setup-account enrollment is
performed. The admin authenticates with its existing password and TOTP to
configure the fixture; its existing grants also allow API sends from
`setup@desktop.test` without completing that account's first-run setup. Use
`/tmp/restmail-devserver/seed.py` only when intentionally creating a new fixture,
not on each stack start.

Resource IDs, certificates, binaries and the mode-0600 environment file live in
`/tmp/restmail-desktop-devstack` (mode 0700). Repeated `up` is a no-op for a live
stack. `down` checks ownership labels and stops/removes only the recorded container
and network IDs; it never stops PostgreSQL, another REST Mail process, or removes
database volumes. Keep the state directory so its CA survives restarts. Optional
overrides must be passed consistently, e.g.
`chore desktop:devstack:up -- --state /tmp/my-desktop-stack --port 18082`.
After changing stack code, run `down`, then `up` to rebuild.

## Desktop automation (MCP)

Development builds enable the embedded MCP server automatically. Production builds
disable it unless launched with `--mcp`, `--mcp-stdio`, or
`RESTMAIL_DESKTOP_MCP=1`. HTTP binds to an available ephemeral port on `127.0.0.1`.
Request a preferred port with `--mcp-port 47822` or
`RESTMAIL_DESKTOP_MCP_PORT=47822`; if it is busy, MCP falls back to an ephemeral
loopback port. Always read the actual URL from `mcp.json`. Host overrides are not
supported. An MCP startup failure is logged and never prevents the app from running.

Every launch creates a fresh MCP bearer token, separate from the mail API token.
Each instance writes `os.UserConfigDir()/REST Mail/mcp-<pid>.json`, containing
`url`, `token`, `pid`, and `version`. The `mcp.json` alias points to the most
recently started live instance. On orderly shutdown, that instance's own file is
removed and the alias is restored to the newest surviving instance, or removed
when none remain. Registry updates use an interprocess lock and atomic file
replacement. On macOS the alias is
`~/Library/Application Support/REST Mail/mcp.json`; on Linux it is normally
`~/.config/REST Mail/mcp.json`; on Windows it is under `%AppData%\REST Mail`.
The directory is mode `0700` and the file is mode `0600` on Unix. The file is
removed on orderly shutdown. Readers must skip entries whose PID is no longer
alive after a crash; Go clients can use `mcp.ReadDiscovery` or
`mcp.ListDiscoveries`, which perform this check. Do not share these files: their
token grants full mail and UI control, including sending and deleting messages. HTTP requires the token
on every request and rejects browser-origin requests. Reconnect after each launch.

After launching `desktop/bin/RESTMail.app/Contents/MacOS/RESTMail --mcp`, connect
Claude Code on macOS (requires `jq`):

```sh
config="$HOME/Library/Application Support/REST Mail/mcp.json"
kill -0 "$(jq -r .pid "$config")" || exit 1
claude mcp add --transport http restmail "$(jq -r .url "$config")" \
  --header "Authorization: Bearer $(jq -r .token "$config")"
```

To target a particular running app, use `mcp-<pid>.json` instead of `mcp.json`.
For an isolated test harness, launch with a dedicated discovery path; this writes
only that file and does not alter the shared registry or alias:

```sh
desktop/bin/RESTMail.app/Contents/MacOS/RESTMail --mcp \
  --mcp-discovery "$HOME/Library/Application Support/REST Mail/test-run.json"
```

REST Mail runs once: launching it again, from any location, brings the running
window forward and exits. An automated test that must run beside the owner's copy
names a separate test copy, whose window title then says "test copy <name>":

```sh
RESTMAIL_DESKTOP_INSTANCE=polish desktop/bin/RESTMail.app/Contents/MacOS/RESTMail --mcp \
  --mcp-discovery /tmp/desktop-polish-mcp.json
```

The name is lowercase letters, digits and dashes, up to 32 characters; anything
else is ignored and the launch joins the single running copy.
Valid named test copies use an ephemeral in-process credential store: they never
restore, rotate, write, or delete the owner's OS-keychain credentials. Test-copy
sign-ins are deliberately not retained after shutdown. Normal launches still use
the system keychain. For native visual tests, change only the generated test
bundle's `Contents/Info.plist` identifier before signing that test copy, to isolate
WebKit website data and preferences. Never change the release bundle identifier,
`com.restmail.desktop`, or the source `build/darwin/Info.plist` for this purpose.

The equivalent setting is `RESTMAIL_DESKTOP_MCP_DISCOVERY=/absolute/path.json`.
Use that exact file when reading the URL and token; it is removed on orderly
shutdown. A discovery-path option alone does not enable MCP in production.

Or let Claude Code launch the app with a private stdio connection:

```sh
claude mcp add --transport stdio restmail -- \
  /absolute/path/to/desktop/bin/RESTMail.app/Contents/MacOS/RESTMail --mcp-stdio
```

Stdio reserves stdout for MCP and sends application logs to stderr. HTTP remains
available for other agents, using an ephemeral port by default. Domain tools
operate through the same Go session as the UI. `list_messages.page` is a one-based
page number (default 1, maximum 100), with 50 messages per page; later pages follow
the server's opaque pagination cursors. UI tools
require the frontend's `mcp:ui:request` / `mcp:ui:response` bridge and time out after
10 seconds if it is not ready. On macOS, `ui_screenshot` first captures this
process's visible window using CoreGraphics and `screencapture`; it falls back to
the bridge's `screenshot` action (`{data: base64PNG}`). Other platforms use the
bridge. Screen Recording permission may be required for native capture.
`app_logs` returns up to 500 recent redacted lines, including tool failure reasons.

## Native macOS controls

The application menu exposes the same message actions as the reader buttons.
Message shortcuts are disabled while typing or when no applicable message is
selected. Standard Edit commands use the webview's native responder chain.

| Shortcut | Action |
| --- | --- |
| ⌘N | New message tab |
| ⌘R / ⇧⌘R / ⇧⌘F | Reply / Reply All / Forward |
| ⌘⌫ / ⌃⌘A | Delete / Archive |
| ⇧⌘U / ⇧⌘L | Toggle read / flag |
| ⇧⌘N | Refresh, following macOS Mail's Get New Mail shortcut |
| ↑ / ↓ / Return | Select / open message |
| ⌘W | Close current draft tab, or the main window on the Mail tab |
| ⌃Tab / ⌃⇧Tab | Next / previous tab, including Mail |

Draft tabs retain independent editors while switching tabs. Closing a dirty tab
or quitting asks to save it through the existing draft API. A failed save keeps
the tab open. Send closes its tab and refreshes the mailbox. Reader and compose
content use the full pane width; these layout changes are desktop-only.

The Dock badge equals the active account's unread Inbox count. After startup or
sign-in, once folders load and notification permission resolves, one notification
says “You have N unread messages” (nothing is posted for zero). New Inbox arrivals
are coalesced in a fixed two-second window: one message shows its sender and
subject and opens that message when clicked; several show “N new messages” and
up to three distinct first senders, and open the Inbox. Unread summaries also
open the Inbox. Notification bodies never contain mail body text or HTML.
Messages already visible in the focused reader are excluded at arrival and again
before posting. Duplicate SSE deliveries are counted once; sign-out, account
switching and teardown cancel pending batches. Native authorization is checked
before every post, and browser notifications remain disabled in the desktop app.
Named test copies log successful native notification payloads and Dock badge
updates, allowing verification when screen sharing suppresses macOS banners.

Desktop account events use a Go-owned SSE connection and Wails runtime events,
not EventSource over the webview's custom asset scheme, which can buffer until
the response closes. The bridge keeps credentials in Go, sanitizes event data,
reconnects with Last-Event-ID, and refreshes folders on reconnect. Subscriptions
are cancelled on component teardown, sign-out and application shutdown. Browser
webmail continues to use its native EventSource connection.

Drop files anywhere in the signed-in window to attach them to the active compose
tab, or create a new tab when Mail is active. The Attach picker follows the same
path. Go retains the bytes in memory per draft; JavaScript receives only IDs,
filenames and sizes. Directories and non-regular files are refused. Until the API
exposes a message-size capability the total limit is 25 MiB per draft. A failed
batch adds nothing. Removing a chip releases its bytes; closing a tab or signing
out clears them. Held files do not survive quitting. Until #2212 provides outgoing
uploads, attachments explicitly disable send, schedule and server draft save;
the close dialog offers Cancel or Don't Save instead of silently losing files.

MCP adds `ui_menu` (`id`, optional `window: "main"`), `ui_tabs`, and
`ui_drop_files` (`paths`). Menu IDs are `new-message`, `close-window`, `refresh`,
`reply`, `reply-all`, `forward`, `archive`, `delete`, `toggle-read`, `toggle-flag`.
`ui_snapshot` exposes `tab-mail`, `tab-<id>`, `tab-close-<id>`, and visible compose
controls. Inactive tab panels and closed disclosures are excluded from UI targets.
No compose windows or `ui_windows` tool are created.

## Release cycle

From the repository root, `chore desktop:dev` runs with live reload and
`chore desktop:build` creates `desktop/bin/RESTMail.app` without installing it.
`chore desktop:release:notes` lists desktop and webmail commits since the commit
in the installed app's `Contents/Resources/release.json`.
`chore desktop:release` prints those notes, builds, installs into `~/Applications`,
and prints the version, commit and UTC build time. `chore desktop:info` prints
the installed build's metadata. `chore desktop:latest` uses authenticated `gh`
to download and install the newest published `desktop-v*` release (Apple Silicon
Macs only). These helpers require Python 3; builds also require Go, Node/pnpm and
the pinned Wails CLI, which the local build task installs when missing.
Override the installation destination for testing, for example
`DESKTOP_INSTALL_DIR=/tmp/restmail-desktop chore desktop:release`.

Versions are `$(cat desktop/VERSION).$(git rev-list --count HEAD)`, currently
`0.1.<repository commit count>`. New commits on the same history increase the
number. Uncommitted changes, including untracked desktop or webmail files, add
`-dirty` to local versions and commits. The binary and `release.json` include
the version, full commit and build timestamp; the macOS bundle also records the
numeric version in its plist. Metadata is embedded before ad-hoc signing.

After building and testing locally, run `chore desktop:publish` to zip the last
local `desktop/bin/RESTMail.app` with `ditto` and publish it with `release.json`.
The tag is `desktop-v<build version>`, its target is the build's recorded commit,
and its title is `REST Mail desktop <version>`. Notes list commits touching
`desktop/` or `webmail/` since the previous desktop tag, through the built commit.
Publishing refuses dirty builds, commits absent from all remote branches, and
existing releases. It fetches remote refs and prints a `git push` command when
needed, but never pushes. Authenticated `gh` is required. `chore desktop:ship`
runs `desktop:release` followed by `desktop:publish`; neither waits for CI.

The path-filtered Desktop workflow builds macOS arm64 (`.app.zip`), Windows
amd64 (`.exe.zip`) and Linux amd64 (`.tar.gz`) for pull requests, pushes to main,
and manual dispatches. Download archives from the workflow's artifacts; CI never
publishes a GitHub release and has read-only contents permissions. Linux uses
its own runner gate and requires GTK 4 and WebKitGTK 6.0 at runtime. Only the
native macOS and Windows build jobs use fixed GitHub-hosted runners. macOS
builds are ad-hoc signed, not notarized; Windows executables are unsigned.
