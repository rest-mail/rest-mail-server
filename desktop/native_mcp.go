package main

import (
	"context"
	"encoding/json"
	"errors"

	desktopmcp "github.com/rest-mail/rest-mail-server/desktop/internal/mcp"
)

type nativeMCP struct {
	native *Native
	bridge *desktopmcp.Bridge
}

func (adapter *nativeMCP) Call(ctx context.Context, action string, args any) (any, error) {
	if action == "dropFiles" {
		data, err := json.Marshal(args)
		if err != nil {
			return nil, err
		}
		var drop desktopmcp.DropFilesArgs
		if err := json.Unmarshal(data, &drop); err != nil {
			return nil, err
		}
		return adapter.native.DropFiles(drop.Paths)
	}
	if action == "menu" {
		data, err := json.Marshal(args)
		if err != nil {
			return nil, err
		}
		var menu desktopmcp.MenuArgs
		if err = json.Unmarshal(data, &menu); err != nil {
			return nil, err
		}
		if menu.Window == "" {
			menu.Window = "main"
		}
		if menu.Window != "" {
			window, exists := adapter.native.app.Window.GetByName(menu.Window)
			if !exists {
				return nil, errors.New("window not found")
			}
			window.Focus()
		}
		return map[string]any{}, adapter.native.invokeMenu(menu.ID)
	}
	return adapter.bridge.Call(ctx, action, args)
}
