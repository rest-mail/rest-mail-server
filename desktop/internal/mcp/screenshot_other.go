//go:build !darwin || !cgo

package mcp

import (
	"context"
	"errors"
)

func nativeScreenshot(context.Context) ([]byte, error) {
	return nil, errors.New("native window capture is not available on this build")
}
