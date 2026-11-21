package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
)

// singleInstanceID names the lock that keeps one copy of this app running. It is
// per executable path: opening the installed app again brings its window
// forward instead of starting a second copy, while a development build from
// another checkout is a different program and runs beside it.
func singleInstanceID(executable string) string {
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	sum := sha256.Sum256([]byte(filepath.Clean(executable)))
	return "com.restmail.desktop." + hex.EncodeToString(sum[:6])
}

func currentExecutable() string {
	executable, err := os.Executable()
	if err != nil {
		return os.Args[0]
	}
	return executable
}
