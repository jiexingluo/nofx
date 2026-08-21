package mcp

// providerRegistry maps provider names to factory functions.
var providerRegistry = map[string]func(...ClientOption) AIClient{}

// RegisterProvider registers a provider factory function.
// Called by provider/payment sub-packages in their init() functions.
func RegisterProvider(name string, factory func(...ClientOption) AIClient) {
	providerRegistry[name] = factory
}

// NewAIClientByProvider creates an AIClient by provider name using the registry.
// Returns nil if the provider is not registered.
func NewAIClientByProvider(name string, opts ...ClientOption) AIClient {
	factory, ok := providerRegistry[name]
	if !ok {
		return nil
	}
	return factory(opts...)
}

// IsRegisteredProvider reports whether name has a registered client factory,
// without constructing one. Used by store.AIModelStore.UpdateWithName to
// recognize a bare catalog provider slug (e.g. "codex_cli") when creating a
// brand-new model row, instead of guessing the provider by splitting the id
// on "_" and taking the last segment - that heuristic silently mis-derives
// "cli" for any provider whose own name contains an underscore.
func IsRegisteredProvider(name string) bool {
	_, ok := providerRegistry[name]
	return ok
}
