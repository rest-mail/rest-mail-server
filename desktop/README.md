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
`RESTMAIL_DESKTOP_MCP=1`. HTTP always binds to `127.0.0.1:47821`; override the port
with `--mcp-port 47822` or `RESTMAIL_DESKTOP_MCP_PORT=47822`. Host overrides are
not supported. A port conflict fails startup rather than exposing another address.

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
available for other agents; use a different `--mcp-port` when another instance is
running. Domain tools operate through the same Go session as the UI. UI tools
require the frontend's `mcp:ui:request` / `mcp:ui:response` bridge and time out after
10 seconds if it is not ready. `ui_screenshot` returns a PNG from its `screenshot`
action (`{data: base64PNG}`). `app_logs` returns up to 500 recent redacted lines.
