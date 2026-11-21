package mcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDiscoveryHelperProcess(t *testing.T) {
	if os.Getenv("RESTMAIL_MCP_DISCOVERY_TEST_CHILD") != "1" {
		return
	}
	io.Copy(io.Discard, os.Stdin)
}

func liveTestProcess(t *testing.T) int {
	t.Helper()
	command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestDiscoveryHelperProcess$")
	command.Env = append(os.Environ(), "RESTMAIL_MCP_DISCOVERY_TEST_CHILD=1")
	input, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { input.Close(); command.Wait() })
	return command.Process.Pid
}

func TestPerInstanceDiscoveryRestoresAnotherLiveInstance(t *testing.T) {
	otherPID := liveTestProcess(t)
	root := t.TempDir()
	directory := filepath.Join(root, "REST Mail")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	other := Discovery{URL: "http://127.0.0.1:47833/mcp", PID: otherPID, Token: "other-launch-token", Version: "other"}
	otherPath := filepath.Join(directory, fmt.Sprintf("mcp-%d.json", otherPID))
	if err := writeDiscoveryFile(otherPath, other); err != nil {
		t.Fatal(err)
	}
	server, err := New(Options{ConfigDir: root})
	if err != nil {
		t.Fatal(err)
	}
	stop, err := server.StartHTTP(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	instancePath := filepath.Join(directory, fmt.Sprintf("mcp-%d.json", os.Getpid()))
	instance, err := ReadDiscovery(instancePath)
	if err != nil || instance.Token != server.token || instance.URL != server.URL() {
		t.Fatalf("instance discovery: %#v %v", instance, err)
	}
	alias, err := ReadDiscovery(filepath.Join(directory, "mcp.json"))
	if err != nil || alias.PID != os.Getpid() {
		t.Fatalf("newest alias: %#v %v", alias, err)
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	alias, err = ReadDiscovery(filepath.Join(directory, "mcp.json"))
	if err != nil || alias != other {
		t.Fatalf("restored alias: %#v %v, want %#v", alias, err, other)
	}
	if _, err := os.Stat(instancePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("own instance file not removed: %v", err)
	}
	if _, err := ReadDiscovery(otherPath); err != nil {
		t.Fatal("another instance was removed", err)
	}
}

func TestDiscoveryReadersSkipDeadProcesses(t *testing.T) {
	command := exec.Command(os.Args[0], "-test.run=^$")
	if err := command.Run(); err != nil {
		t.Fatal(err)
	}
	deadPID := command.Process.Pid
	if processAlive(deadPID) {
		t.Fatal("completed child is still considered alive")
	}
	root := t.TempDir()
	directory := filepath.Join(root, "REST Mail")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	dead := Discovery{URL: "http://127.0.0.1:47831/mcp", PID: deadPID, Token: "dead"}
	deadPath := filepath.Join(directory, fmt.Sprintf("mcp-%d.json", deadPID))
	if err := writeDiscoveryFile(deadPath, dead); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadDiscovery(deadPath); err == nil {
		t.Fatal("reader accepted dead process")
	}
	alive := Discovery{URL: "http://127.0.0.1:47832/mcp", PID: os.Getpid(), Token: "alive"}
	if err := writeDiscoveryFile(filepath.Join(directory, fmt.Sprintf("mcp-%d.json", os.Getpid())), alive); err != nil {
		t.Fatal(err)
	}
	instances, err := ListDiscoveries(root)
	if err != nil || len(instances) != 1 || instances[0] != alive {
		t.Fatalf("live discovery list: %#v %v", instances, err)
	}
	if processAlive(0) || processAlive(-1) {
		t.Fatal("invalid PID accepted")
	}
}

func TestDiscoveryChoosesNewestLiveInstanceOnShutdown(t *testing.T) {
	root := t.TempDir()
	server, err := New(Options{ConfigDir: root})
	if err != nil {
		t.Fatal(err)
	}
	stop, err := server.StartHTTP(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	directory := filepath.Join(root, "REST Mail")
	var newest Discovery
	for index := 0; index < 2; index++ {
		pid := liveTestProcess(t)
		candidate := Discovery{URL: "http://127.0.0.1:47835/mcp", PID: pid, Token: fmt.Sprintf("candidate-%d", index)}
		path := filepath.Join(directory, fmt.Sprintf("mcp-%d.json", pid))
		if err := writeDiscoveryFile(path, candidate); err != nil {
			t.Fatal(err)
		}
		stamp := time.Now().Add(time.Duration(index) * time.Minute)
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
		newest = candidate
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	alias, err := ReadDiscovery(filepath.Join(directory, "mcp.json"))
	if err != nil || alias != newest {
		t.Fatalf("did not choose latest live instance: %#v %v", alias, err)
	}
}

func TestCustomDiscoveryPathIsolatesHarness(t *testing.T) {
	root := t.TempDir()
	custom := filepath.Join(t.TempDir(), "harness", "desktop.json")
	server, err := New(Options{ConfigDir: root, DiscoveryPath: custom})
	if err != nil {
		t.Fatal(err)
	}
	stop, err := server.StartHTTP(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	discovery, err := ReadDiscovery(custom)
	if err != nil || discovery.URL != server.URL() {
		t.Fatalf("custom discovery: %#v %v", discovery, err)
	}
	connect(t, discovery)
	if _, err := os.Stat(filepath.Join(root, "REST Mail")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("custom launch touched shared discovery: %v", err)
	}
	info, err := os.Stat(custom)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("custom discovery permissions: %v %v", info, err)
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(custom); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("custom discovery not cleaned: %v", err)
	}
}

func TestLaunchDiscoveryPath(t *testing.T) {
	for _, args := range [][]string{{"--mcp-discovery", "/tmp/restmail-test.json"}, {"--mcp-discovery=/tmp/restmail-test.json"}} {
		launch, err := ParseLaunch(args, func(string) string { return "" })
		if err != nil || launch.DiscoveryPath != "/tmp/restmail-test.json" {
			t.Fatalf("launch discovery: %#v %v", launch, err)
		}
	}
	for _, args := range [][]string{{"--mcp-discovery"}, {"--mcp-discovery="}} {
		if _, err := ParseLaunch(args, func(string) string { return "" }); err == nil {
			t.Fatalf("missing path accepted: %v", args)
		}
	}
	launch, err := ParseLaunch(nil, func(key string) string {
		if strings.HasSuffix(key, "_DISCOVERY") {
			return "/tmp/from-env.json"
		}
		return ""
	})
	if err != nil || launch.DiscoveryPath != "/tmp/from-env.json" {
		t.Fatalf("environment discovery: %#v %v", launch, err)
	}
}
