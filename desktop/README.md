# REST Mail desktop

The Wails application reuses `../webmail/src` through Vite and TypeScript aliases.
Go owns authentication, the OS keychain and the relative API proxy. JavaScript
receives identity and setup state, never access or refresh tokens.

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
