package mcp

import (
	"fmt"
	"strconv"
	"strings"
)

type Launch struct {
	Enabled bool
	Stdio   bool
	Port    int
}

func ParseLaunch(args []string, getenv func(string) string) (Launch, error) {
	launch := Launch{Enabled: developmentBuild || getenv("RESTMAIL_DESKTOP_MCP") == "1"}
	port := getenv("RESTMAIL_DESKTOP_MCP_PORT")
	for index := 0; index < len(args); index++ {
		switch argument := args[index]; {
		case argument == "--mcp":
			launch.Enabled = true
		case argument == "--mcp-stdio":
			launch.Enabled = true
			launch.Stdio = true
		case argument == "--mcp-port":
			index++
			if index == len(args) {
				return launch, fmt.Errorf("--mcp-port requires a port")
			}
			port = args[index]
		case strings.HasPrefix(argument, "--mcp-port="):
			port = strings.TrimPrefix(argument, "--mcp-port=")
		}
	}
	if port != "" {
		parsed, err := strconv.Atoi(port)
		if err != nil || parsed < 1 || parsed > 65535 {
			return launch, fmt.Errorf("MCP port must be between 1 and 65535")
		}
		launch.Port = parsed
	}
	return launch, nil
}
