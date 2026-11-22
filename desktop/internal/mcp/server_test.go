package mcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type bearerTransport struct{ token string }

func (transport bearerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.Header.Set("Authorization", "Bearer "+transport.token)
	return http.DefaultTransport.RoundTrip(clone)
}

func startTestServer(t *testing.T, options Options) (*Server, Discovery) {
	t.Helper()
	options.ConfigDir = t.TempDir()
	server, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	closeServer, err := server.StartHTTP(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := closeServer(); err != nil {
			t.Error(err)
		}
	})
	data, err := os.ReadFile(filepath.Join(options.ConfigDir, "REST Mail", "mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	var discovery Discovery
	if err := json.Unmarshal(data, &discovery); err != nil {
		t.Fatal(err)
	}
	return server, discovery
}

func connect(t *testing.T, discovery Discovery) *sdk.ClientSession {
	t.Helper()
	client := sdk.NewClient(&sdk.Implementation{Name: "desktop-mcp-test", Version: "1"}, nil)
	session, err := client.Connect(t.Context(), &sdk.StreamableClientTransport{Endpoint: discovery.URL, HTTPClient: &http.Client{Transport: bearerTransport{discovery.Token}}, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

func call(t *testing.T, client *sdk.ClientSession, name string, args any) *sdk.CallToolResult {
	t.Helper()
	result, err := client.CallTool(t.Context(), &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func resultText(t *testing.T, result *sdk.CallToolResult) string {
	t.Helper()
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestHTTPRequiresTokenAndBindsLoopback(t *testing.T) {
	server, discovery := startTestServer(t, Options{})
	address := strings.TrimSuffix(strings.TrimPrefix(discovery.URL, "http://"), "/mcp")
	host, _, err := net.SplitHostPort(address)
	if err != nil || !net.ParseIP(host).IsLoopback() || host != "127.0.0.1" {
		t.Fatalf("non-loopback listener: %q", address)
	}
	for _, token := range []string{"", "wrong", discovery.Token} {
		request, _ := http.NewRequest(http.MethodPost, discovery.URL, strings.NewReader(`{}`))
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if token != discovery.Token && response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("token %q: HTTP %d", token, response.StatusCode)
		}
		if token == discovery.Token && response.StatusCode == http.StatusUnauthorized {
			t.Fatal("valid token refused")
		}
	}
	client := sdk.NewClient(&sdk.Implementation{Name: "unauthorized"}, nil)
	_, err = client.Connect(t.Context(), &sdk.StreamableClientTransport{Endpoint: discovery.URL}, nil)
	if err == nil {
		t.Fatal("MCP client connected without token")
	}
	connect(t, discovery)
	if server.URL() != discovery.URL {
		t.Fatal("discovery URL mismatch")
	}
}

var domainCases = []struct{ name, args string }{
	{"session_status", `{}`},
	{"sign_in", `{"server":"https://mail.test","email":"a@mail.test","password":"password-value"}`},
	{"submit_totp", `{"code":"123456"}`},
	{"sign_out", `{}`},
	{"list_folders", `{}`},
	{"list_messages", `{"folder":"INBOX","page":2,"query":"hello"}`},
	{"get_message", `{"id":"42"}`},
	{"search", `{"query":"hello"}`},
	{"send_message", `{"to":["to@mail.test"],"cc":["cc@mail.test"],"subject":"hello","body":"plain","html":"<p>html</p>"}`},
	{"save_draft", `{"to":["to@mail.test"],"subject":"draft","body":"plain"}`},
	{"set_flags", `{"ids":["1","2"],"seen":false,"flagged":true}`},
	{"move_messages", `{"ids":["1","2"],"folder":"Archive"}`},
	{"delete_messages", `{"ids":["1","2"]}`},
}

func TestToolListComplete(t *testing.T) {
	_, discovery := startTestServer(t, Options{})
	list, err := connect(t, discovery).ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var expected []string
	for _, test := range domainCases {
		expected = append(expected, test.name)
	}
	expected = append(expected, "thunderbird_profiles", "thunderbird_import", "import_status", "add_imap_account", "list_accounts", "remove_imap_account", "ui_snapshot", "ui_menu", "ui_tabs", "ui_theme_list", "ui_theme_set", "ui_theme_get", "ui_drop_files", "ui_click", "ui_type", "ui_navigate", "ui_read_text", "ui_screenshot", "app_info", "app_logs")
	var actual []string
	for _, tool := range list.Tools {
		actual = append(actual, tool.Name)
	}
	slices.Sort(expected)
	slices.Sort(actual)
	if !reflect.DeepEqual(expected, actual) {
		t.Fatalf("tools = %v, want %v", actual, expected)
	}
}

func TestEveryDomainToolArgumentsAndErrors(t *testing.T) {
	for _, test := range domainCases {
		t.Run(test.name, func(t *testing.T) {
			var receivedName string
			var receivedArgs json.RawMessage
			var domainErr error
			_, discovery := startTestServer(t, Options{Domain: DomainFunc(func(ctx context.Context, name string, args json.RawMessage) (any, error) {
				receivedName, receivedArgs = name, args
				return map[string]any{"ok": true}, domainErr
			})})
			client := connect(t, discovery)
			var args map[string]any
			json.Unmarshal([]byte(test.args), &args)
			result := call(t, client, test.name, args)
			if result.IsError {
				t.Fatalf("unexpected error: %s", resultText(t, result))
			}
			var received map[string]any
			json.Unmarshal(receivedArgs, &received)
			if receivedName != test.name || !reflect.DeepEqual(received, args) {
				t.Fatalf("received %s %s, want %s %s", receivedName, receivedArgs, test.name, test.args)
			}
			domainErr = errors.New("upstream failed with API-TOKEN-SECRET")
			result = call(t, client, test.name, args)
			if !result.IsError || !strings.Contains(resultText(t, result), "domain_error") || strings.Contains(resultText(t, result), "API-TOKEN-SECRET") {
				t.Fatalf("unsafe error mapping: %s", resultText(t, result))
			}
		})
	}
}

func TestToolResultsAndLogsNeverContainAPIToken(t *testing.T) {
	const secret = "api-bearer-secret-value"
	server, discovery := startTestServer(t, Options{Secrets: func() []string { return []string{secret} }, Domain: DomainFunc(func(context.Context, string, json.RawMessage) (any, error) {
		return map[string]any{"access_token": secret, "nested": []any{map[string]any{"text": "prefix " + secret + " suffix", "password": "other-secret"}}}, nil
	})})
	client := connect(t, discovery)
	for _, test := range domainCases {
		var args map[string]any
		json.Unmarshal([]byte(test.args), &args)
		text := resultText(t, call(t, client, test.name, args))
		if strings.Contains(text, secret) || strings.Contains(text, "other-secret") {
			t.Fatalf("%s leaked credential: %s", test.name, text)
		}
	}
	server.logs.Write([]byte("Authorization: Bearer " + secret + "\npassword=hidden\n"))
	text := resultText(t, call(t, client, "app_logs", map[string]any{"lines": 20}))
	if strings.Contains(text, secret) || strings.Contains(text, "hidden") {
		t.Fatalf("log leaked credential: %s", text)
	}
	data, _ := os.ReadFile(filepath.Join(server.options.ConfigDir, "REST Mail", "mcp.json"))
	if bytes.Contains(data, []byte(secret)) {
		t.Fatal("discovery leaked API token")
	}
}

func TestUIBridgeRoundTripAndTimeout(t *testing.T) {
	var bridge *Bridge
	bridge = NewBridge(func(request UIRequest) {
		if request.Action == "readText" {
			return
		}
		bridge.Respond(UIResponse{ID: "unrelated", OK: true})
		bridge.Respond(UIResponse{ID: request.ID, OK: true, Result: map[string]any{"route": "/inbox", "action": request.Action, "args": request.Args}})
	})
	_, discovery := startTestServer(t, Options{UI: bridge, Timeout: 30 * time.Millisecond})
	client := connect(t, discovery)
	for _, test := range []struct {
		name   string
		args   map[string]any
		action string
	}{
		{"ui_snapshot", map[string]any{}, "snapshot"},
		{"ui_menu", map[string]any{"id": "reply", "window": "main"}, "menu"},
		{"ui_tabs", map[string]any{}, "tabs"},
		{"ui_theme_list", map[string]any{}, "themeList"},
		{"ui_theme_get", map[string]any{}, "themeGet"},
		{"ui_theme_set", map[string]any{"theme": "Midnight"}, "themeSet"},
		{"ui_theme_set", map[string]any{"remix": map[string]any{"colours": "canopy", "typefaces": "letterpress", "shape": "sorbet", "overrides": map[string]any{"accent": "#ffc187", "radius": float64(0), "density": float64(42), "reading": "Literata"}}}, "themeSet"},
		{"ui_drop_files", map[string]any{"paths": []any{"/tmp/test-attachment.txt"}}, "dropFiles"},
		{"ui_type", map[string]any{"window": "main", "testId": "subject", "text": "active tab"}, "type"},
		{"ui_click", map[string]any{"testId": "compose"}, "click"},
		{"ui_type", map[string]any{"testId": "subject", "text": "hello"}, "type"},
		{"ui_navigate", map[string]any{"route": "/drafts"}, "navigate"},
	} {
		result := call(t, client, test.name, test.args)
		if result.IsError {
			t.Fatalf("%s: %s", test.name, resultText(t, result))
		}
		var output map[string]any
		json.Unmarshal([]byte(result.Content[0].(*sdk.TextContent).Text), &output)
		if output["action"] != test.action || !reflect.DeepEqual(output["args"], test.args) {
			t.Fatalf("round trip: %#v", output)
		}
	}
	result := call(t, client, "ui_read_text", map[string]any{"testId": "missing"})
	if !result.IsError || !strings.Contains(resultText(t, result), "ui_timeout") {
		t.Fatalf("missing timeout: %s", resultText(t, result))
	}
	bridge.mutex.Lock()
	defer bridge.mutex.Unlock()
	if len(bridge.pending) != 0 {
		t.Fatal("pending UI requests leaked")
	}
}

func TestScreenshotReturnsPNG(t *testing.T) {
	var buffer bytes.Buffer
	png.Encode(&buffer, image.NewRGBA(image.Rect(0, 0, 2, 2)))
	result := screenshotResult(map[string]any{"data": base64.StdEncoding.EncodeToString(buffer.Bytes())})
	content, ok := result.Content[0].(*sdk.ImageContent)
	if result.IsError || !ok || content.MIMEType != "image/png" || !bytes.Equal(content.Data, buffer.Bytes()) {
		t.Fatalf("not a PNG: %#v", result)
	}
	if !screenshotResult(map[string]any{"data": "not png"}).IsError {
		t.Fatal("invalid screenshot accepted")
	}
	bridge := NewBridge(func(UIRequest) {})
	bridge.emit = func(request UIRequest) {
		bridge.Respond(UIResponse{ID: request.ID, OK: true, Result: map[string]any{"data": base64.StdEncoding.EncodeToString(buffer.Bytes())}})
	}
	_, discovery := startTestServer(t, Options{UI: bridge})
	result = call(t, connect(t, discovery), "ui_screenshot", map[string]any{})
	content, ok = result.Content[0].(*sdk.ImageContent)
	if result.IsError || !ok || !bytes.Equal(content.Data, buffer.Bytes()) {
		t.Fatal("HTTP screenshot differs from bridge PNG")
	}
}

func TestUnavailableAndFailedTools(t *testing.T) {
	_, discovery := startTestServer(t, Options{})
	client := connect(t, discovery)
	for _, name := range []string{"session_status", "ui_snapshot"} {
		if !call(t, client, name, map[string]any{}).IsError {
			t.Fatalf("%s unexpectedly available", name)
		}
	}
	bridge := NewBridge(func(UIRequest) {})
	bridge.emit = func(request UIRequest) { bridge.Respond(UIResponse{ID: request.ID, Error: "Bearer secret"}) }
	_, discovery = startTestServer(t, Options{UI: bridge, Domain: DomainFunc(func(context.Context, string, json.RawMessage) (any, error) { return nil, context.DeadlineExceeded })})
	client = connect(t, discovery)
	for _, test := range []struct{ name, code string }{{"ui_snapshot", "ui_error"}, {"session_status", "domain_timeout"}} {
		result := call(t, client, test.name, map[string]any{})
		if !result.IsError || !strings.Contains(resultText(t, result), test.code) || strings.Contains(resultText(t, result), "secret") {
			t.Fatalf("%s: %s", test.name, resultText(t, result))
		}
	}
	if _, err := New(Options{Port: -1}); err == nil {
		t.Fatal("invalid port accepted")
	}
}

func TestDiscoveryPermissionsRotationAndCleanup(t *testing.T) {
	server, discovery := startTestServer(t, Options{Info: Info{Version: "test-version"}})
	path := filepath.Join(server.options.ConfigDir, "REST Mail", "mcp.json")
	for _, test := range []struct {
		path string
		mode os.FileMode
	}{{path, 0600}, {filepath.Dir(path), 0700}} {
		info, err := os.Stat(test.path)
		if err != nil || info.Mode().Perm() != test.mode {
			t.Fatalf("permissions %s: %v %v", test.path, info, err)
		}
	}
	if len(discovery.Token) != 64 || discovery.PID != os.Getpid() || discovery.Version != "test-version" {
		t.Fatalf("bad discovery metadata: %#v", discovery)
	}
	second, err := New(Options{ConfigDir: server.options.ConfigDir})
	if err != nil {
		t.Fatal(err)
	}
	if second.token == server.token {
		t.Fatal("launch token reused")
	}
	closeSecond, err := second.StartHTTP(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := closeSecond(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("discovery not removed: %v", err)
	}
}

func TestStdioTransport(t *testing.T) {
	server, err := New(Options{Info: Info{Version: "stdio-version"}})
	if err != nil {
		t.Fatal(err)
	}
	serverPipe, clientPipe := net.Pipe()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- server.RunStdio(ctx, serverPipe, serverPipe) }()
	client := sdk.NewClient(&sdk.Implementation{Name: "stdio-test"}, nil)
	session, err := client.Connect(ctx, &sdk.IOTransport{Reader: clientPipe, Writer: clientPipe}, nil)
	if err != nil {
		t.Fatal(err)
	}
	result := call(t, session, "app_info", map[string]any{})
	if result.IsError || !strings.Contains(resultText(t, result), "stdio-version") {
		t.Fatalf("stdio result: %s", resultText(t, result))
	}
	session.Close()
	clientPipe.Close()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stdio failed to stop")
	}
}

func TestLaunchConfiguration(t *testing.T) {
	for _, test := range []struct {
		args           []string
		env            map[string]string
		enabled, stdio bool
		port           int
		invalid        bool
	}{
		{enabled: developmentBuild, port: 0},
		{args: []string{"--mcp"}, enabled: true, port: 0},
		{args: []string{"--mcp-stdio", "--mcp-port", "47822"}, enabled: true, stdio: true, port: 47822},
		{env: map[string]string{"RESTMAIL_DESKTOP_MCP": "1", "RESTMAIL_DESKTOP_MCP_PORT": "47823"}, enabled: true, port: 47823},
		{args: []string{"--mcp-port=47824"}, env: map[string]string{"RESTMAIL_DESKTOP_MCP_PORT": "47823"}, enabled: developmentBuild, port: 47824},
		{args: []string{"--mcp-port"}, invalid: true},
		{args: []string{"--mcp-port=0"}, invalid: true},
		{args: []string{"--mcp-port=0.0.0.0:47821"}, invalid: true},
		{args: []string{"--mcp-port=65536"}, invalid: true},
	} {
		got, err := ParseLaunch(test.args, func(name string) string { return test.env[name] })
		if test.invalid {
			if err == nil {
				t.Fatalf("accepted %#v", test.args)
			}
			continue
		}
		if err != nil || got.Enabled != test.enabled || got.Stdio != test.stdio || got.Port != test.port {
			t.Fatalf("launch %#v: %#v %v", test, got, err)
		}
	}
}

func TestLogBufferBoundedAndRedacted(t *testing.T) {
	buffer := NewLogBuffer(2, nil)
	io.WriteString(buffer, "first\nsecond\nAuthorization: Bearer secret\n")
	lines := buffer.Recent(100)
	if len(lines) != 2 || lines[0] != "second" || strings.Contains(lines[1], "secret") {
		t.Fatalf("bad log buffer: %v", lines)
	}
	if len(buffer.Recent(1)) != 1 {
		t.Fatal("line limit ignored")
	}
}

func TestBusyRequestedPortFallsBackToEphemeral(t *testing.T) {
	occupied, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	server, discovery := startTestServer(t, Options{Port: occupied.Addr().(*net.TCPAddr).Port})
	if strings.Contains(discovery.URL, occupied.Addr().String()+"/") {
		t.Fatal("discovery retained busy port")
	}
	if !strings.Contains(strings.Join(server.logs.Recent(20), "\n"), "falling back") {
		t.Fatal("fallback not logged")
	}
	connect(t, discovery)
}

func TestStartupFailureIsRecordedInLogs(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "not-a-directory")
	if err != nil {
		t.Fatal(err)
	}
	file.Close()
	server, err := New(Options{ConfigDir: file.Name()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.StartHTTP(t.Context()); err == nil {
		t.Fatal("expected discovery failure")
	}
	if !strings.Contains(strings.Join(server.logs.Recent(20), "\n"), "MCP HTTP startup failed") {
		t.Fatal("startup failure not logged")
	}
}
