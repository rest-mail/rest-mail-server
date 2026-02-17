.PHONY: build build-all build-tui build-tui-mac build-api build-gateway clean

# Default build (current platform)
build:
	go build -o bin/api ./cmd/api/
	go build -o bin/gateway ./cmd/gateway/
	go build -o bin/tui ./cmd/tui/

# Build for macOS Apple Silicon (arm64)
build-tui-mac:
	GOOS=darwin GOARCH=arm64 go build -o bin/tui-darwin-arm64 ./cmd/tui/

# Build for macOS Intel
build-tui-mac-intel:
	GOOS=darwin GOARCH=amd64 go build -o bin/tui-darwin-amd64 ./cmd/tui/

# Build for Linux arm64
build-tui-linux:
	GOOS=linux GOARCH=arm64 go build -o bin/tui-linux-arm64 ./cmd/tui/

# Build all API server
build-api:
	go build -o bin/api ./cmd/api/

# Build gateway
build-gateway:
	go build -o bin/gateway ./cmd/gateway/

# Build TUI for current platform
build-tui:
	go build -o bin/tui ./cmd/tui/

# Build all targets including macOS
build-all: build build-tui-mac build-tui-mac-intel

# Run tests
test:
	go test ./...

# Run e2e tests (requires Docker environment)
test-e2e:
	go test ./tests/e2e/ -v -timeout 5m

# Clean build artifacts
clean:
	rm -rf bin/
