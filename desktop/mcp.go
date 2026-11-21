package main

import (
	"context"
	"io"
	"log"
	"os"

	"github.com/rest-mail/rest-mail-server/desktop/internal/apiclient"
	desktopmcp "github.com/rest-mail/rest-mail-server/desktop/internal/mcp"
	"github.com/rest-mail/rest-mail-server/desktop/internal/session"
	"github.com/wailsapp/wails/v3/pkg/application"
)

func startMCP(app *application.App, manager *session.Manager, client *apiclient.Client) func() {
	launch, err := desktopmcp.ParseLaunch(os.Args[1:], os.Getenv)
	if err != nil {
		log.Printf("Desktop MCP configuration invalid; continuing without MCP: %v", err)
		return func() {}
	}
	if !launch.Enabled {
		return func() {}
	}
	bridge := desktopmcp.NewBridge(func(request desktopmcp.UIRequest) { app.Event.Emit("mcp:ui:request", request) })
	unsubscribe := app.Event.On("mcp:ui:response", func(event *application.CustomEvent) { bridge.Respond(event.Data) })
	server, err := desktopmcp.New(desktopmcp.Options{
		Port:          launch.Port,
		DiscoveryPath: launch.DiscoveryPath,
		Info:          desktopmcp.Info{Version: version, Commit: commit, BuiltAt: builtAt},
		UI:            bridge,
		Domain:        desktopmcp.NewDomain(manager, client),
		Secrets:       manager.SensitiveValues,
	})
	if err != nil {
		unsubscribe()
		log.Printf("Desktop MCP initialization failed; continuing without MCP: %v", err)
		return func() {}
	}
	previousLog := log.Writer()
	log.SetOutput(io.MultiWriter(previousLog, server.Logs()))
	ctx, cancel := context.WithCancel(context.Background())
	closeHTTP := startMCPHTTP(ctx, server)
	if launch.Stdio {
		output := os.Stdout
		os.Stdout = os.Stderr
		go func() {
			if err := server.RunStdio(ctx, os.Stdin, output); err != nil {
				log.Print("MCP stdio stopped")
			}
		}()
	}
	return func() {
		cancel()
		closeHTTP()
		unsubscribe()
		log.SetOutput(previousLog)
	}
}

func startMCPHTTP(ctx context.Context, server *desktopmcp.Server) func() error {
	closeHTTP, err := server.StartHTTP(ctx)
	if err != nil {
		log.Printf("Desktop MCP HTTP unavailable; continuing application: %v", err)
		return func() error { return nil }
	}
	return closeHTTP
}
