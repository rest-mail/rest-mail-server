package main

import (
	"context"
	"embed"
	"os"

	"log"

	"github.com/rest-mail/rest-mail-server/desktop/internal/apiclient"
	"github.com/rest-mail/rest-mail-server/desktop/internal/apiproxy"
	"github.com/rest-mail/rest-mail-server/desktop/internal/localmail"
	"github.com/rest-mail/rest-mail-server/desktop/internal/native"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

// Wails uses Go's `embed` package to embed the frontend files into the binary.
// Any files in the frontend/dist folder will be embedded into the binary and
// made available to the frontend.
// See https://pkg.go.dev/embed for more information.

//go:embed all:frontend/dist
var assets embed.FS

func init() {
	// Register a custom event whose associated data type is string.
	// This is not required, but the binding generator will pick up registered events
	// and provide a strongly typed JS/TS API for them.
	application.RegisterEvent[string]("time")
}

// main function serves as the application's entry point. It initializes the application, creates a window,
// and starts a goroutine that emits a time-based event every second. It subsequently runs the application and
// logs any error that might occur.
func main() {
	nativeUI := newNative()

	// Create a new Wails application by providing the necessary options.
	// Variables 'Name' and 'Description' are for application metadata.
	// 'Assets' configures the asset server with the 'FS' variable pointing to the frontend files.
	// 'Bind' is a list of Go struct instances. The frontend has access to the methods of these instances.
	// 'Mac' options tailor the application when running an macOS.
	options := application.Options{
		Name:        "REST Mail",
		Description: "REST Mail desktop client",
		Services: []application.Service{
			application.NewService(nativeUI),
			application.NewService(&AppInfo{}),
			// BEGIN UI native file service
			application.NewService(&Files{}),
			// END UI native file service
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
		ShouldQuit: nativeUI.shouldQuit,
	}
	// One copy of REST Mail: opening it again, from anywhere, brings this window forward.
	var mainWindow *application.WebviewWindow
	options.SingleInstance = &application.SingleInstanceOptions{
		UniqueID: singleInstanceID(currentInstance()),
		OnSecondInstanceLaunch: func(data application.SecondInstanceData) {
			if link := native.MailtoArgument(data.Args); link != "" {
				nativeUI.openMailto(link)
			}
			if mainWindow != nil {
				mainWindow.UnMinimise()
				mainWindow.Show()
				mainWindow.Focus()
			}
		},
	}

	// Desktop core: share this manager and client with native integrations, including MCP.
	sessionManager := newDesktopSession()
	config, err := os.UserConfigDir()
	if err != nil {
		log.Fatal("Could not locate local mail storage")
	}
	localMail, err := localmail.New(sessionManager, config, currentInstance())
	if err != nil {
		log.Fatal("Could not create local mail storage")
	}
	defer localMail.Close()
	sessionManager.SetOnRemove(localMail.Forget)
	mailClient := apiclient.New(localMail)
	imports := newImports(sessionManager, localMail)
	defer imports.service.Close()
	options.Services = append(options.Services, application.NewService(imports))
	mailStreams := &MailStreams{manager: sessionManager}
	application.RegisterEvent[SessionStatus]("session:changed")
	sessionService := NewSession(sessionManager)
	sessionService.onChange = func() {
		go func() {
			if err := localMail.Activate(); err != nil {
				log.Print("Local mail storage unavailable")
			}
		}()
	}
	options.Services = append(options.Services, application.NewService(sessionService))
	options.Services = append(options.Services, application.NewService(&LocalMail{service: localMail, manager: sessionManager}))
	options.Services = append(options.Services, application.NewService(&Accounts{manager: sessionManager}))
	options.Services = append(options.Services, application.NewService(mailStreams))
	options.Assets.Middleware = apiproxy.New(localMail)
	// End desktop core.
	app := application.New(options)
	localContext, stopLocal := context.WithCancel(context.Background())
	go localMail.Run(localContext)
	app.OnShutdown(stopLocal)
	app.OnShutdown(sessionManager.Close)
	app.OnShutdown(mailStreams.Close)
	app.Event.On("session:changed", func(event *application.CustomEvent) {
		if status, ok := event.Data.(SessionStatus); ok && !status.SignedIn {
			mailStreams.Close()
		}
	})
	nativeUI.install(app)
	// macOS delivers mailto: links as an open-URL event; Windows and Linux pass
	// them as an argument, which Wails reports only for links containing "://".
	app.Event.OnApplicationEvent(events.Common.ApplicationLaunchedWithUrl, func(event *application.ApplicationEvent) {
		if link := event.Context().URL(); native.IsMailto(link) {
			nativeUI.openMailto(link)
		}
	})
	if link := native.MailtoArgument(os.Args[1:]); link != "" {
		nativeUI.openMailto(link)
	}

	// Embedded MCP service registration.
	stopMCP := startMCP(app, sessionManager, mailClient, nativeUI, imports)
	app.OnShutdown(stopMCP)
	defer stopMCP()
	// End embedded MCP service registration.

	// Create a new window with the necessary options.
	// 'Title' is the title of the window.
	// 'Mac' options tailor the window when running on macOS.
	// 'BackgroundColour' is the background colour of the window.
	// 'URL' is the URL that will be loaded into the webview.
	mainWindow = app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:           "main",
		EnableFileDrop: true,
		Title:          windowTitle("REST Mail "+version, currentInstance()),
		Width:          1200,
		Height:         800,
		MinWidth:       1000,
		MinHeight:      720,
		Mac: application.MacWindow{
			InvisibleTitleBarHeight: 50,
			Backdrop:                application.MacBackdropTranslucent,
			TitleBar:                application.MacTitleBarHiddenInset,
		},
		BackgroundColour: application.NewRGB(6, 7, 15),
		URL:              "/",
	})
	mainWindow.OnWindowEvent(events.Common.WindowFocus, func(*application.WindowEvent) { nativeUI.updateMenu() })
	mainWindow.RegisterHook(events.Common.WindowClosing, func(event *application.WindowEvent) {
		if !nativeUI.shouldQuit() {
			event.Cancel()
		}
	})
	mainWindow.OnWindowEvent(events.Common.WindowFilesDropped, func(event *application.WindowEvent) {
		if _, err := nativeUI.DropFiles(event.Context().DroppedFiles()); err != nil {
			app.Event.Emit("native:drop-error", err.Error())
		}
	})

	// Run the application. This blocks until the application has been exited.
	err = app.Run()

	// If an error occurred while running the application, log it and exit.
	if err != nil {
		log.Fatal(err)
	}
}
