package mcp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
)

type UIRequest struct {
	ID     string `json:"id"`
	Action string `json:"action"`
	Args   any    `json:"args"`
}

type UIResponse struct {
	ID     string `json:"id"`
	OK     bool   `json:"ok"`
	Result any    `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}

type Bridge struct {
	emit    func(UIRequest)
	mutex   sync.Mutex
	pending map[string]chan UIResponse
}

func NewBridge(emit func(UIRequest)) *Bridge {
	return &Bridge{emit: emit, pending: make(map[string]chan UIResponse)}
}

func (bridge *Bridge) Respond(payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	var response UIResponse
	if json.Unmarshal(data, &response) != nil {
		return
	}
	bridge.mutex.Lock()
	channel := bridge.pending[response.ID]
	bridge.mutex.Unlock()
	if channel != nil {
		select {
		case channel <- response:
		default:
		}
	}
}

func (bridge *Bridge) Call(ctx context.Context, action string, args any) (any, error) {
	identifier := make([]byte, 16)
	if _, err := rand.Read(identifier); err != nil {
		return nil, errors.New("UI request ID unavailable")
	}
	id := hex.EncodeToString(identifier)
	channel := make(chan UIResponse, 1)
	bridge.mutex.Lock()
	bridge.pending[id] = channel
	bridge.mutex.Unlock()
	defer func() { bridge.mutex.Lock(); delete(bridge.pending, id); bridge.mutex.Unlock() }()
	bridge.emit(UIRequest{ID: id, Action: action, Args: args})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case response := <-channel:
		if !response.OK {
			return nil, errors.New("UI action failed")
		}
		return response.Result, nil
	}
}
