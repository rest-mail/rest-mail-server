package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func ReadDiscovery(path string) (Discovery, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Discovery{}, err
	}
	var discovery Discovery
	if err := json.Unmarshal(data, &discovery); err != nil {
		return Discovery{}, err
	}
	endpoint, err := url.Parse(discovery.URL)
	if err != nil || endpoint.Scheme != "http" || endpoint.Hostname() != "127.0.0.1" || endpoint.Port() == "" || endpoint.Path != "/mcp" || discovery.Token == "" || !processAlive(discovery.PID) {
		return Discovery{}, errors.New("MCP discovery is invalid or its process is no longer alive")
	}
	return discovery, nil
}

func ListDiscoveries(configDir string) ([]Discovery, error) {
	if configDir == "" {
		var err error
		configDir, err = os.UserConfigDir()
		if err != nil {
			return nil, err
		}
	}
	return liveDiscoveries(filepath.Join(configDir, "REST Mail"))
}

func liveDiscoveries(directory string) ([]Discovery, error) {
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return []Discovery{}, nil
	}
	if err != nil {
		return nil, err
	}
	type candidate struct {
		discovery Discovery
		modified  time.Time
	}
	var candidates []candidate
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "mcp-") || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		discovery, err := ReadDiscovery(filepath.Join(directory, entry.Name()))
		if err != nil || entry.Name() != fmt.Sprintf("mcp-%d.json", discovery.PID) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		candidates = append(candidates, candidate{discovery, info.ModTime()})
	}
	sort.Slice(candidates, func(left, right int) bool { return candidates[left].modified.After(candidates[right].modified) })
	result := make([]Discovery, len(candidates))
	for index, candidate := range candidates {
		result[index] = candidate.discovery
	}
	return result, nil
}

func writeDiscoveryFile(path string, discovery Discovery) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".mcp-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err := json.NewEncoder(file).Encode(discovery); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func removeDiscoveryFile(path, token string) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var current Discovery
	if json.Unmarshal(data, &current) != nil || current.Token != token {
		return nil
	}
	return os.Remove(path)
}

func withDiscoveryLock(directory string, update func() error) error {
	file, err := os.OpenFile(filepath.Join(directory, ".mcp.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	unlock, err := lockDiscovery(file)
	if err != nil {
		return err
	}
	defer unlock()
	return update()
}

func (server *Server) registerDiscovery() (func() error, error) {
	discovery := Discovery{URL: server.URL(), Token: server.token, PID: os.Getpid(), Version: server.options.Info.Version}
	if path := server.options.DiscoveryPath; path != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return nil, err
		}
		if err := writeDiscoveryFile(path, discovery); err != nil {
			return nil, err
		}
		return func() error { return removeDiscoveryFile(path, server.token) }, nil
	}
	root := server.options.ConfigDir
	if root == "" {
		var err error
		root, err = os.UserConfigDir()
		if err != nil {
			return nil, err
		}
	}
	directory := filepath.Join(root, "REST Mail")
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	if err := os.Chmod(directory, 0700); err != nil {
		return nil, err
	}
	instancePath := filepath.Join(directory, fmt.Sprintf("mcp-%d.json", discovery.PID))
	aliasPath := filepath.Join(directory, "mcp.json")
	if err := withDiscoveryLock(directory, func() error {
		if previous, err := ReadDiscovery(aliasPath); err == nil && previous.PID != discovery.PID {
			previousPath := filepath.Join(directory, fmt.Sprintf("mcp-%d.json", previous.PID))
			if _, err := os.Stat(previousPath); errors.Is(err, os.ErrNotExist) {
				if err := writeDiscoveryFile(previousPath, previous); err != nil {
					return err
				}
			}
		}
		if err := writeDiscoveryFile(instancePath, discovery); err != nil {
			return err
		}
		if err := writeDiscoveryFile(aliasPath, discovery); err != nil {
			return errors.Join(err, removeDiscoveryFile(instancePath, server.token))
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return func() error {
		return withDiscoveryLock(directory, func() error {
			if err := removeDiscoveryFile(instancePath, server.token); err != nil {
				return err
			}
			instances, err := liveDiscoveries(directory)
			if err != nil {
				return err
			}
			if len(instances) > 0 {
				return writeDiscoveryFile(aliasPath, instances[0])
			}
			if err := os.Remove(aliasPath); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			return nil
		})
	}, nil
}
