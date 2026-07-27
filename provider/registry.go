package provider

import (
	"fmt"
	"strings"
	"sync"
)

// ProviderRegistry is the thread-safe Go equivalent of Pi's dynamic provider
// registration. Registered providers override built-ins until unregistered.
type ProviderRegistry struct {
	mu       sync.RWMutex
	fallback ProviderFactory
	entries  map[string]Provider
}

func NewProviderRegistry(fallback ProviderFactory) *ProviderRegistry {
	return &ProviderRegistry{fallback: fallback, entries: map[string]Provider{}}
}

func (r *ProviderRegistry) Provider(name string, timeoutMS int) (Provider, error) {
	return r.ProviderForModel(name, "", timeoutMS)
}

func (r *ProviderRegistry) ProviderForModel(name, model string, timeoutMS int) (Provider, error) {
	key := strings.ToLower(strings.TrimSpace(name))
	r.mu.RLock()
	provider := r.entries[key]
	r.mu.RUnlock()
	if provider != nil {
		return provider, nil
	}
	if r.fallback == nil {
		return nil, fmt.Errorf("provider %q is not registered", name)
	}
	if factory, ok := r.fallback.(ModelProviderFactory); ok {
		return factory.ProviderForModel(name, model, timeoutMS)
	}
	return r.fallback.Provider(name, timeoutMS)
}

func (r *ProviderRegistry) Register(name string, provider Provider) error {
	key := strings.ToLower(strings.TrimSpace(name))
	if key == "" || provider == nil {
		return fmt.Errorf("provider name and implementation are required")
	}
	r.mu.Lock()
	r.entries[key] = provider
	r.mu.Unlock()
	return nil
}

func (r *ProviderRegistry) Unregister(name string) {
	r.mu.Lock()
	delete(r.entries, strings.ToLower(strings.TrimSpace(name)))
	r.mu.Unlock()
}
