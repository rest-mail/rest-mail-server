package mcp

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type Domain interface {
	Call(context.Context, string, json.RawMessage) (any, error)
}

type DomainFunc func(context.Context, string, json.RawMessage) (any, error)

func (function DomainFunc) Call(ctx context.Context, name string, args json.RawMessage) (any, error) {
	return function(ctx, name, args)
}

type UI interface {
	Call(context.Context, string, any) (any, error)
}

type Info struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	BuiltAt string `json:"builtAt"`
	MCPURL  string `json:"mcpUrl"`
}

type Options struct {
	Port      int
	ConfigDir string
	Info      Info
	Domain    Domain
	UI        UI
	Secrets   func() []string
	Timeout   time.Duration
}

type Discovery struct {
	URL     string `json:"url"`
	Token   string `json:"token"`
	PID     int    `json:"pid"`
	Version string `json:"version"`
}

type Server struct {
	protocol *sdk.Server
	options  Options
	token    string
	mutex    sync.RWMutex
	url      string
	logs     *LogBuffer
}

func New(options Options) (*Server, error) {
	if options.Port < 0 || options.Port > 65535 {
		return nil, errors.New("MCP port must be between 0 and 65535")
	}
	if options.Timeout <= 0 {
		options.Timeout = 10 * time.Second
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, errors.New("generate MCP token")
	}
	server := &Server{options: options, token: hex.EncodeToString(secret)}
	server.logs = NewLogBuffer(500, server.redact)
	server.protocol = sdk.NewServer(&sdk.Implementation{Name: "restmail-desktop", Version: options.Info.Version}, nil)
	server.registerTools()
	return server, nil
}

func (server *Server) URL() string {
	server.mutex.RLock()
	defer server.mutex.RUnlock()
	return server.url
}

func (server *Server) Logs() *LogBuffer { return server.logs }

func (server *Server) Handler() http.Handler {
	transport := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server.protocol }, &sdk.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if subtle.ConstantTimeCompare([]byte(request.Header.Get("Authorization")), []byte("Bearer "+server.token)) != 1 {
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		if request.URL.Path != "/mcp" {
			http.NotFound(writer, request)
			return
		}
		if origin := request.Header.Get("Origin"); origin != "" {
			http.Error(writer, "browser origins are not allowed", http.StatusForbidden)
			return
		}
		request.Body = http.MaxBytesReader(writer, request.Body, 2<<20)
		transport.ServeHTTP(writer, request)
	})
}

func (server *Server) StartHTTP(ctx context.Context) (stop func() error, startupErr error) {
	defer func() {
		if startupErr != nil {
			server.logs.Write([]byte("MCP HTTP startup failed: " + startupErr.Error() + "\n"))
		}
	}()
	listener, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(server.options.Port)))
	if err != nil && server.options.Port != 0 {
		server.logs.Write([]byte("MCP requested port unavailable; falling back to a loopback ephemeral port\n"))
		listener, err = net.Listen("tcp4", "127.0.0.1:0")
	}
	if err != nil {
		return nil, fmt.Errorf("listen for desktop MCP: %w", err)
	}
	server.mutex.Lock()
	server.url = "http://" + listener.Addr().String() + "/mcp"
	server.mutex.Unlock()
	path, err := server.writeDiscovery()
	if err != nil {
		listener.Close()
		return nil, err
	}
	httpServer := &http.Server{Handler: server.Handler(), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
	go func() {
		if err := httpServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			server.logs.Write([]byte("MCP listener stopped unexpectedly\n"))
		}
	}()
	var once sync.Once
	var closeErr error
	closed := make(chan struct{})
	closeServer := func() error {
		once.Do(func() {
			close(closed)
			closeErr = httpServer.Close()
			data, err := os.ReadFile(path)
			var current Discovery
			if err == nil && json.Unmarshal(data, &current) == nil && current.Token == server.token {
				if err := os.Remove(path); closeErr == nil {
					closeErr = err
				}
			}
		})
		return closeErr
	}
	go func() {
		select {
		case <-ctx.Done():
			closeServer()
		case <-closed:
		}
	}()
	server.logs.Write([]byte("MCP listening on " + server.URL() + "\n"))
	return closeServer, nil
}

func (server *Server) RunStdio(ctx context.Context, input io.ReadCloser, output io.WriteCloser) error {
	return server.protocol.Run(ctx, &sdk.IOTransport{Reader: input, Writer: output})
}

func (server *Server) writeDiscovery() (string, error) {
	root := server.options.ConfigDir
	if root == "" {
		var err error
		root, err = os.UserConfigDir()
		if err != nil {
			return "", err
		}
	}
	directory := filepath.Join(root, "REST Mail")
	if err := os.MkdirAll(directory, 0700); err != nil {
		return "", err
	}
	if err := os.Chmod(directory, 0700); err != nil {
		return "", err
	}
	file, err := os.CreateTemp(directory, ".mcp-*.json")
	if err != nil {
		return "", err
	}
	defer os.Remove(file.Name())
	if err := json.NewEncoder(file).Encode(Discovery{URL: server.URL(), Token: server.token, PID: os.Getpid(), Version: server.options.Info.Version}); err != nil {
		file.Close()
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	path := filepath.Join(directory, "mcp.json")
	if err := os.Rename(file.Name(), path); err != nil {
		return "", err
	}
	return path, nil
}

func (server *Server) redact(text string) string {
	text = strings.ReplaceAll(text, server.token, "[REDACTED]")
	if server.options.Secrets != nil {
		for _, secret := range server.options.Secrets() {
			if secret != "" {
				text = strings.ReplaceAll(text, secret, "[REDACTED]")
			}
		}
	}
	return redactCredentials(text)
}

func (server *Server) result(value any) *sdk.CallToolResult {
	data, err := json.Marshal(value)
	if err != nil {
		return toolError("result_encoding_failed")
	}
	var safe any
	if err := json.Unmarshal(data, &safe); err != nil {
		return toolError("result_encoding_failed")
	}
	safe = scrubFields(safe)
	data, err = json.Marshal(safe)
	if err != nil {
		return toolError("result_encoding_failed")
	}
	return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: server.redact(string(data))}}}
}

func toolError(code string) *sdk.CallToolResult {
	return &sdk.CallToolResult{IsError: true, Content: []sdk.Content{&sdk.TextContent{Text: code}}}
}
