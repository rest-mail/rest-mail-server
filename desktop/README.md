# Welcome to Your New Wails3 Project!

Congratulations on generating your Wails3 application! This README will guide you through the next steps to get your project up and running.

## Getting Started

1. Navigate to your project directory in the terminal.

2. To run your application in development mode, use the following command:

   ```
   wails3 dev
   ```

   This will start your application and enable hot-reloading for both frontend and backend changes.

3. To build your application for production, use:

   ```
   wails3 build
   ```

   This will create a production-ready executable in the `build` directory.

## Exploring Wails3 Features

Now that you have your project set up, it's time to explore the features that Wails3 offers:

1. **Check out the examples**: The best way to learn is by example. Visit the `examples` directory in the `v3/examples` directory to see various sample applications.

2. **Run an example**: To run any of the examples, navigate to the example's directory and use:

   ```
   go run .
   ```

   Note: Some examples may be under development during the alpha phase.

3. **Explore the documentation**: Visit the [Wails3 documentation](https://v3.wails.io/) for in-depth guides and API references.

4. **Join the community**: Have questions or want to share your progress? Join the [Wails Discord](https://discord.gg/JDdSxwjhGf) or visit the [Wails discussions on GitHub](https://github.com/wailsapp/wails/discussions).

## Project Structure

Take a moment to familiarize yourself with your project structure:

- `frontend/`: Contains your frontend code (HTML, CSS, JavaScript/TypeScript)
- `main.go`: The entry point of your Go backend
- `app.go`: Define your application structure and methods here
- `wails.json`: Configuration file for your Wails project

## Next Steps

1. Modify the frontend in the `frontend/` directory to create your desired UI.
2. Add backend functionality in `main.go`.
3. Use `wails3 dev` to see your changes in real-time.
4. When ready, build your application with `wails3 build`.

Happy coding with Wails3! If you encounter any issues or have questions, don't hesitate to consult the documentation or reach out to the Wails community.

## Desktop automation (MCP)

Development builds enable the embedded MCP server automatically. Production builds
disable it unless launched with `--mcp`, `--mcp-stdio`, or
`RESTMAIL_DESKTOP_MCP=1`. HTTP binds to an available ephemeral port on `127.0.0.1`.
Request a preferred port with `--mcp-port 47822` or
`RESTMAIL_DESKTOP_MCP_PORT=47822`; if it is busy, MCP falls back to an ephemeral
loopback port. Always read the actual URL from `mcp.json`. Host overrides are not
supported. An MCP startup failure is logged and never prevents the app from running.

Every launch creates a fresh MCP bearer token, separate from the mail API token.
The user-only discovery file is `os.UserConfigDir()/REST Mail/mcp.json`, containing
`url`, `token`, `pid`, and `version`. On macOS this is
`~/Library/Application Support/REST Mail/mcp.json`; on Linux it is normally
`~/.config/REST Mail/mcp.json`; on Windows it is under `%AppData%\REST Mail`.
The directory is mode `0700` and the file is mode `0600` on Unix. The file is
removed on orderly shutdown. Do not share this file: its token grants full mail
and UI control, including sending and deleting messages. HTTP requires the token
on every request and rejects browser-origin requests. Reconnect after each launch.

After launching `desktop/bin/RESTMail.app/Contents/MacOS/RESTMail --mcp`, connect
Claude Code on macOS (requires `jq`):

```sh
config="$HOME/Library/Application Support/REST Mail/mcp.json"
claude mcp add --transport http restmail "$(jq -r .url "$config")" \
  --header "Authorization: Bearer $(jq -r .token "$config")"
```

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
Macs only). These helpers require Python 3; builds also require Go, Node/npm and
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
