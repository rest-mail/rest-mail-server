package main

import (
	"bytes"
	"context"
	"log"
	"os"
	"strings"
	"testing"

	desktopmcp "github.com/rest-mail/rest-mail-server/desktop/internal/mcp"
)

func TestMCPConfigurationFailureDoesNotExitApp(t *testing.T) {
	previousArgs, previousWriter := os.Args, log.Writer()
	defer func() { os.Args = previousArgs; log.SetOutput(previousWriter) }()
	os.Args = []string{"RESTMail", "--mcp", "--mcp-port=invalid"}
	var output bytes.Buffer
	log.SetOutput(&output)
	stop := startMCP(nil, nil, nil)
	stop()
	if !strings.Contains(output.String(), "continuing without MCP") {
		t.Fatalf("startup failure not logged: %s", output.String())
	}
}

func TestMCPStartupFailureKeepsApplicationAlive(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "not-a-directory")
	if err != nil {
		t.Fatal(err)
	}
	file.Close()
	server, err := desktopmcp.New(desktopmcp.Options{ConfigDir: file.Name()})
	if err != nil {
		t.Fatal(err)
	}
	stop := startMCPHTTP(context.Background(), server)
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(server.Logs().Recent(10), "\n"), "MCP HTTP startup failed") {
		t.Fatal("startup failure absent from app_logs buffer")
	}
}
