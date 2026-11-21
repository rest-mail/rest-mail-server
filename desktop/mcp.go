package main

import (
	"context"
	"io"
	"log"
	"os"

	desktopmcp "github.com/rest-mail/rest-mail-server/desktop/internal/mcp"
	"github.com/wailsapp/wails/v3/pkg/application"
)

func startMCP(app *application.App) func() {
	launch, err := desktopmcp.ParseLaunch(os.Args[1:], os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	if !launch.Enabled {
		return func() {}
	}
	bridge := desktopmcp.NewBridge(func(request desktopmcp.UIRequest) { app.Event.Emit("mcp:ui:request", request) })
	unsubscribe := app.Event.On("mcp:ui:response", func(event *application.CustomEvent) { bridge.Respond(event.Data) })
	server, err := desktopmcp.New(desktopmcp.Options{
		Port: launch.Port,
		Info: desktopmcp.Info{Version: version, Commit: commit, BuiltAt: builtAt},
		UI:   bridge,
	})
	if err != nil {
		log.Fatal(err)
	}
	previousLog := log.Writer()
	log.SetOutput(io.MultiWriter(previousLog, server.Logs()))
	ctx, cancel := context.WithCancel(context.Background())
	closeHTTP, err := server.StartHTTP(ctx)
	if err != nil {
		cancel()
		log.Fatal(err)
	}
	if launch.Stdio {
		output := os.Stdout
		os.Stdout = os.Stderr
		go func() {
			if err := server.RunStdio(ctx, os.Stdin, output); err != nil {
				log.Print("MCP stdio stopped")
			}
			app.Quit()
		}()
	}
	return func() {
		cancel()
		closeHTTP()
		unsubscribe()
		log.SetOutput(previousLog)
	}
}
