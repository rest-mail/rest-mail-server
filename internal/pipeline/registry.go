package pipeline

import (
	"fmt"
	"sync"
)

// Registry holds all registered filter factories.
type Registry struct {
	mu        sync.RWMutex
	factories map[string]FilterFactory
}

// NewRegistry creates a new filter registry.
func NewRegistry() *Registry {
	return &Registry{
		factories: make(map[string]FilterFactory),
	}
}

// Register adds a filter factory to the registry.
func (r *Registry) Register(name string, factory FilterFactory) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.factories[name] = factory
}

// Create instantiates a filter from its name and configuration.
func (r *Registry) Create(name string, config []byte) (Filter, error) {
	r.mu.RLock()
	factory, ok := r.factories[name]
	r.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("unknown filter: %s", name)
	}
	return factory(config)
}

// List returns all registered filter names.
func (r *Registry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.factories))
	for name := range r.factories {
		names = append(names, name)
	}
	return names
}

// DefaultRegistry is the global filter registry.
var DefaultRegistry = NewRegistry()
