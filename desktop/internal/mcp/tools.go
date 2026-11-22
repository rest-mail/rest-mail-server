package mcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image/png"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rest-mail/rest-mail-server/desktop/internal/imapmail"
	"github.com/rest-mail/rest-mail-server/desktop/internal/thunderbird"
)

type EmptyArgs struct {
	Window string `json:"window,omitempty"`
}

type ImportStatusArgs struct {
	Cancel bool `json:"cancel,omitempty"`
}

type MenuArgs struct {
	ID     string `json:"id"`
	Window string `json:"window,omitempty"`
}

type DropFilesArgs struct {
	Paths []string `json:"paths"`
}

type AccountArgs struct {
	AccountID uint `json:"account_id,omitempty"`
}

type SignInArgs struct {
	Server   string `json:"server"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

type TOTPArgs struct {
	Code string `json:"code"`
}

type ListMessagesArgs struct {
	AccountID uint   `json:"account_id,omitempty"`
	Folder    string `json:"folder"`
	Page      int    `json:"page,omitempty"`
	Query     string `json:"query,omitempty"`
}

type MessageArgs struct {
	ID string `json:"id"`
}
type SearchArgs struct {
	AccountID uint   `json:"account_id,omitempty"`
	Query     string `json:"query"`
}

type ComposeArgs struct {
	AccountID uint     `json:"account_id,omitempty"`
	To        []string `json:"to"`
	CC        []string `json:"cc,omitempty"`
	Subject   string   `json:"subject"`
	Body      string   `json:"body"`
	HTML      string   `json:"html,omitempty"`
}

type FlagsArgs struct {
	IDs     []string `json:"ids"`
	Seen    *bool    `json:"seen,omitempty"`
	Flagged *bool    `json:"flagged,omitempty"`
}

type MoveArgs struct {
	IDs    []string `json:"ids"`
	Folder string   `json:"folder"`
}

type DeleteArgs struct {
	IDs []string `json:"ids"`
}
type ElementArgs struct {
	Window string `json:"window,omitempty"`
	TestID string `json:"testId"`
}
type TypeArgs struct {
	Window string `json:"window,omitempty"`
	TestID string `json:"testId"`
	Text   string `json:"text"`
}
type NavigateArgs struct {
	Window string `json:"window,omitempty"`
	Route  string `json:"route"`
}
type LogsArgs struct {
	Lines int `json:"lines,omitempty"`
}

type ThemeOverrides struct {
	Accent  string `json:"accent,omitempty"`
	Display string `json:"display,omitempty"`
	UI      string `json:"ui,omitempty"`
	Reading string `json:"reading,omitempty"`
	Mono    string `json:"mono,omitempty"`
	Radius  *int   `json:"radius,omitempty"`
	Density *int   `json:"density,omitempty"`
}

type ThemeRemix struct {
	Colours   string          `json:"colours"`
	Typefaces string          `json:"typefaces"`
	Shape     string          `json:"shape"`
	Overrides *ThemeOverrides `json:"overrides,omitempty"`
}

type ThemeSetArgs struct {
	Window string      `json:"window,omitempty"`
	Theme  string      `json:"theme,omitempty"`
	Remix  *ThemeRemix `json:"remix,omitempty"`
}

func domainTool[Args any](server *Server, name, description string) {
	sdk.AddTool(server.protocol, &sdk.Tool{Name: name, Description: description}, func(ctx context.Context, request *sdk.CallToolRequest, args Args) (*sdk.CallToolResult, any, error) {
		if server.options.Domain == nil {
			return server.toolFailure(name, "domain_unavailable", nil), nil, nil
		}
		data, err := json.Marshal(args)
		if err != nil {
			return server.toolFailure(name, "invalid_arguments", err), nil, nil
		}
		ctx, cancel := context.WithTimeout(ctx, server.options.Timeout)
		defer cancel()
		value, err := server.options.Domain.Call(ctx, name, data)
		if err != nil {
			code := domainErrorCode(err)
			return server.toolFailure(name, code, err), nil, nil
		}
		return server.result(value), nil, nil
	})
}

func uiTool[Args any](server *Server, name, action string) {
	sdk.AddTool(server.protocol, &sdk.Tool{Name: name, Description: "Drive the desktop UI: " + action}, func(ctx context.Context, request *sdk.CallToolRequest, args Args) (*sdk.CallToolResult, any, error) {
		if action == "screenshot" {
			// Native capture gets its own deadline, so a capture that runs out
			// of time still leaves the bridge fallback a full one.
			nativeCtx, cancelNative := context.WithTimeout(ctx, server.options.Timeout)
			data, err := server.options.Screenshot(nativeCtx)
			cancelNative()
			if err == nil {
				result := screenshotResult(map[string]any{"data": base64.StdEncoding.EncodeToString(data)})
				if !result.IsError {
					return result, nil, nil
				}
				err = errors.New("native capture returned invalid PNG")
			}
			server.logs.Write([]byte(name + ": native capture unavailable; trying UI bridge: " + err.Error() + "\n"))
		}
		if server.options.UI == nil {
			return server.toolFailure(name, "ui_unavailable", nil), nil, nil
		}
		ctx, cancel := context.WithTimeout(ctx, server.options.Timeout)
		defer cancel()
		value, err := server.options.UI.Call(ctx, action, args)
		if err != nil {
			code := "ui_error"
			if errors.Is(err, context.DeadlineExceeded) {
				code = "ui_timeout"
			}
			return server.toolFailure(name, code, err), nil, nil
		}
		if action == "screenshot" {
			result := screenshotResult(value)
			if result.IsError {
				return server.toolFailure(name, "invalid_screenshot", errors.New("UI bridge returned invalid PNG")), nil, nil
			}
			return result, nil, nil
		}
		return server.result(value), nil, nil
	})
}

func screenshotResult(value any) *sdk.CallToolResult {
	encoded, err := json.Marshal(value)
	if err != nil {
		return toolError("invalid_screenshot")
	}
	var image struct {
		Data string `json:"data"`
	}
	if json.Unmarshal(encoded, &image) != nil || len(image.Data) > 12<<20 {
		return toolError("invalid_screenshot")
	}
	data, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(image.Data, "data:image/png;base64,"))
	if err != nil {
		return toolError("invalid_screenshot")
	}
	if _, err := png.DecodeConfig(bytes.NewReader(data)); err != nil {
		return toolError("invalid_screenshot")
	}
	return &sdk.CallToolResult{Content: []sdk.Content{&sdk.ImageContent{Data: data, MIMEType: "image/png"}}}
}

func (server *Server) registerTools() {
	domainTool[imapmail.AccountInput](server, "add_imap_account", "Add an IMAP/SMTP account using verified TLS; password is stored in the OS keychain and never returned")
	domainTool[EmptyArgs](server, "list_accounts", "List configured REST Mail and IMAP accounts without passwords")
	domainTool[AccountArgs](server, "remove_imap_account", "Remove an IMAP account and its keychain password")
	domainTool[EmptyArgs](server, "session_status", "Get the signed-in server and email, never credentials")
	domainTool[SignInArgs](server, "sign_in", "Sign in to a REST Mail server")
	domainTool[TOTPArgs](server, "submit_totp", "Complete two-factor sign-in")
	domainTool[EmptyArgs](server, "sign_out", "Sign out and clear saved credentials")
	domainTool[EmptyArgs](server, "thunderbird_profiles", "Discover read-only Thunderbird profiles, accounts and folders; poll while scanning for message counts")
	domainTool[thunderbird.Selection](server, "thunderbird_import", "Start a background import of selected Thunderbird folder IDs into a REST Mail account; rerun safely to resume using server deduplication")
	domainTool[ImportStatusArgs](server, "import_status", "Read persisted import progress and summary, or cancel the current import with cancel=true")
	domainTool[AccountArgs](server, "list_folders", "List mail folders; account_id optionally selects a configured account")
	domainTool[ListMessagesArgs](server, "list_messages", "List a folder's messages with paging and query")
	domainTool[MessageArgs](server, "get_message", "Read a message")
	domainTool[SearchArgs](server, "search", "Search messages on the server")
	domainTool[ComposeArgs](server, "send_message", "Send a message to the supplied recipients")
	domainTool[ComposeArgs](server, "save_draft", "Save a message as a draft")
	domainTool[FlagsArgs](server, "set_flags", "Set seen and flagged states; omitted flags stay unchanged")
	domainTool[MoveArgs](server, "move_messages", "Move messages to a folder")
	domainTool[DeleteArgs](server, "delete_messages", "Delete the supplied messages")
	uiTool[EmptyArgs](server, "ui_snapshot", "snapshot")
	uiTool[MenuArgs](server, "ui_menu", "menu")
	uiTool[EmptyArgs](server, "ui_tabs", "tabs")
	uiTool[EmptyArgs](server, "ui_theme_list", "themeList")
	uiTool[ThemeSetArgs](server, "ui_theme_set", "themeSet")
	uiTool[EmptyArgs](server, "ui_theme_get", "themeGet")
	uiTool[DropFilesArgs](server, "ui_drop_files", "dropFiles")
	uiTool[ElementArgs](server, "ui_click", "click")
	uiTool[TypeArgs](server, "ui_type", "type")
	uiTool[NavigateArgs](server, "ui_navigate", "navigate")
	uiTool[ElementArgs](server, "ui_read_text", "readText")
	uiTool[EmptyArgs](server, "ui_screenshot", "screenshot")
	sdk.AddTool(server.protocol, &sdk.Tool{Name: "app_info", Description: "Get desktop version, commit, build time and MCP URL"}, func(ctx context.Context, request *sdk.CallToolRequest, args EmptyArgs) (*sdk.CallToolResult, any, error) {
		info := server.options.Info
		info.MCPURL = server.URL()
		return server.result(info), nil, nil
	})
	sdk.AddTool(server.protocol, &sdk.Tool{Name: "app_logs", Description: "Read recent redacted application log lines (maximum 500)"}, func(ctx context.Context, request *sdk.CallToolRequest, args LogsArgs) (*sdk.CallToolResult, any, error) {
		return server.result(server.logs.Recent(args.Lines)), nil, nil
	})
}
