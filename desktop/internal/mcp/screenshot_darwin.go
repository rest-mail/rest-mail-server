//go:build darwin && cgo

package mcp

/*
#cgo LDFLAGS: -framework CoreGraphics -framework CoreFoundation
#include <CoreGraphics/CoreGraphics.h>
#include <unistd.h>

static unsigned int restmailWindowID(void) {
    CFArrayRef windows = CGWindowListCopyWindowInfo(kCGWindowListOptionOnScreenOnly, kCGNullWindowID);
    if (!windows) return 0;
    unsigned int result = 0;
    for (CFIndex index = 0; index < CFArrayGetCount(windows); index++) {
        CFDictionaryRef window = (CFDictionaryRef)CFArrayGetValueAtIndex(windows, index);
        CFNumberRef owner = (CFNumberRef)CFDictionaryGetValue(window, kCGWindowOwnerPID);
        CFNumberRef layer = (CFNumberRef)CFDictionaryGetValue(window, kCGWindowLayer);
        CFNumberRef number = (CFNumberRef)CFDictionaryGetValue(window, kCGWindowNumber);
        int pid = 0, level = -1;
        if (!owner || !layer || !number) continue;
        CFNumberGetValue(owner, kCFNumberIntType, &pid);
        CFNumberGetValue(layer, kCFNumberIntType, &level);
        if (pid == getpid() && level == 0) {
            CFNumberGetValue(number, kCGWindowIDCFNumberType, &result);
            break;
        }
    }
    CFRelease(windows);
    return result;
}
*/
import "C"

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
)

func nativeScreenshot(ctx context.Context) ([]byte, error) {
	window := uint32(C.restmailWindowID())
	if window == 0 {
		return nil, errors.New("no visible window owned by this process")
	}
	file, err := os.CreateTemp("", "restmail-window-*.png")
	if err != nil {
		return nil, err
	}
	defer os.Remove(file.Name())
	if err := file.Close(); err != nil {
		return nil, err
	}
	output, err := exec.CommandContext(ctx, "/usr/sbin/screencapture", "-x", "-o", "-l"+strconv.FormatUint(uint64(window), 10), file.Name()).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("native window capture failed: %w: %s", err, output)
	}
	info, err := os.Stat(file.Name())
	if err != nil {
		return nil, err
	}
	if info.Size() > 9<<20 {
		return nil, errors.New("native screenshot exceeds 9 MiB")
	}
	return os.ReadFile(file.Name())
}
