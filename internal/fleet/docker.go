// Package fleet drives rest-mail stacks from their manifests.
//
// It exists to replace the Taskfile control plane: a manifest is read directly
// at run time, so which config to act on is an ordinary argument rather than an
// environment variable that has to be visible before a YAML file is parsed.
package fleet

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// State is the lifecycle of one container.
type State string

const (
	StateUp     State = "up"
	StateDown   State = "down"
	StateAbsent State = "absent"
	// StateExists describes a thing with no run state of its own — a docker
	// network is either there or it isn't.
	StateExists State = "exists"
)

// Glyph is the status marker used in rendered output.
func (s State) Glyph() string {
	switch s {
	case StateUp, StateExists:
		return "●"
	case StateDown:
		return "○"
	default:
		return "·"
	}
}

// Container is the subset of docker's view that the fleet commands need.
type Container struct {
	Name   string
	Image  string
	Ports  string
	State  State
	Mounts []string
}

// Docker is the container runtime seam. Everything above this interface is pure,
// so the status and dispatch logic is testable without a daemon.
type Docker interface {
	// Inspect returns the named container. A missing container is not an error:
	// it comes back with State StateAbsent.
	Inspect(ctx context.Context, name string) (Container, error)
	// List returns every container, running or not.
	List(ctx context.Context) ([]Container, error)
	// NetworkExists reports whether a docker network is present.
	NetworkExists(ctx context.Context, name string) (bool, error)
}

// CLI runs the real docker binary. Chosen over the Docker SDK deliberately: it
// keeps the dependency footprint at zero and the commands match the ones the
// Taskfile catalog has been running, so behaviour is easy to compare during the
// port.
type CLI struct {
	Bin string // defaults to "docker"
}

func (c CLI) bin() string {
	if c.Bin == "" {
		return "docker"
	}
	return c.Bin
}

// inspectFormat keeps the field order in lockstep with parseInspect.
const inspectFormat = `{{.State.Running}}` + "\x1f" +
	`{{.Config.Image}}` + "\x1f" +
	`{{range .Mounts}}{{.Source}},{{end}}`

func (c CLI) Inspect(ctx context.Context, name string) (Container, error) {
	out, err := exec.CommandContext(ctx, c.bin(), "inspect", "--format", inspectFormat, name).Output()
	if err != nil {
		// docker exits non-zero for "no such object", which is a normal answer
		// here rather than a failure: the container simply does not exist.
		return Container{Name: name, State: StateAbsent}, nil
	}
	return parseInspect(name, string(out)), nil
}

func parseInspect(name, out string) Container {
	fields := strings.Split(strings.TrimSpace(out), "\x1f")
	c := Container{Name: name, State: StateAbsent}
	if len(fields) < 3 {
		return c
	}
	switch strings.TrimSpace(fields[0]) {
	case "true":
		c.State = StateUp
	case "false":
		c.State = StateDown
	}
	c.Image = strings.TrimSpace(fields[1])
	for _, m := range strings.Split(fields[2], ",") {
		if m = strings.TrimSpace(m); m != "" {
			c.Mounts = append(c.Mounts, m)
		}
	}
	return c
}

func (c CLI) List(ctx context.Context) ([]Container, error) {
	const format = `{{.Names}}` + "\x1f" + `{{.Image}}` + "\x1f" + `{{.Ports}}` + "\x1f" + `{{.State}}`
	out, err := exec.CommandContext(ctx, c.bin(), "ps", "-a", "--format", format).Output()
	if err != nil {
		return nil, fmt.Errorf("docker ps: %w", err)
	}
	var cs []Container
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		f := strings.Split(line, "\x1f")
		if len(f) < 4 {
			continue
		}
		state := StateDown
		if f[3] == "running" {
			state = StateUp
		}
		cs = append(cs, Container{Name: f[0], Image: f[1], Ports: f[2], State: state})
	}
	return cs, nil
}

func (c CLI) NetworkExists(ctx context.Context, name string) (bool, error) {
	out, err := exec.CommandContext(ctx, c.bin(), "network", "ls", "--format", "{{.Name}}").Output()
	if err != nil {
		return false, fmt.Errorf("docker network ls: %w", err)
	}
	for _, n := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.TrimSpace(n) == name {
			return true, nil
		}
	}
	return false, nil
}
