package engine

import (
	"fmt"

	providerpkg "github.com/ageniti/ergo-agent/provider"
)

// These aliases form the private bridge between the execution engine and the
// public provider contracts. They do not introduce a second provider
// abstraction or wrap runtime state.
type (
	ProviderHTTPError      = providerpkg.ProviderHTTPError
	ImageGenerationRequest = providerpkg.ImageGenerationRequest
	ImageGenerationResult  = providerpkg.ImageGenerationResult
	ImageGenerator         = providerpkg.ImageGenerator
	CompletionRequest      = providerpkg.CompletionRequest
	Completion             = providerpkg.Completion
	CompletionDelta        = providerpkg.CompletionDelta
	ModelPricing           = providerpkg.ModelPricing
	Provider               = providerpkg.Provider
	StreamingProvider      = providerpkg.StreamingProvider
	ProviderFactory        = providerpkg.ProviderFactory
	ModelProviderFactory   = providerpkg.ModelProviderFactory
	HTTPProviderFactory    = providerpkg.HTTPProviderFactory
	ProviderRegistry       = providerpkg.ProviderRegistry
)

func NewProviderRegistry(fallback ProviderFactory) *ProviderRegistry {
	return providerpkg.NewProviderRegistry(fallback)
}

func NewOpenRouterImageGeneratorFromEnv() ImageGenerator {
	return providerpkg.NewOpenRouterImageGeneratorFromEnv()
}

func (r *Runtime) RegisterProvider(name string, provider Provider) error {
	registry, ok := r.Providers.(*ProviderRegistry)
	if !ok {
		return fmt.Errorf("runtime provider factory is not mutable")
	}
	return registry.Register(name, provider)
}

func (r *Runtime) UnregisterProvider(name string) {
	if registry, ok := r.Providers.(*ProviderRegistry); ok {
		registry.Unregister(name)
	}
}

func providerFromFactory(factory ProviderFactory, name, model string, timeoutMS int) (Provider, error) {
	if modelFactory, ok := factory.(ModelProviderFactory); ok {
		return modelFactory.ProviderForModel(name, model, timeoutMS)
	}
	return factory.Provider(name, timeoutMS)
}

func cloneHeaders(source map[string]string) map[string]string {
	result := make(map[string]string, len(source)+2)
	for key, value := range source {
		result[key] = value
	}
	return result
}

func mergeHeaders(base, override map[string]string) map[string]string {
	if base == nil {
		base = map[string]string{}
	}
	for key, value := range override {
		if value == "" {
			delete(base, key)
			continue
		}
		base[key] = value
	}
	return base
}
