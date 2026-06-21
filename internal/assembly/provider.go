package assembly

import (
	"context"
	"encoding/json"
	"strings"

	"aurora-capcompute/aurora"
	"aurora-dispatchers/builtin"
	"aurora-dispatchers/registry"
	"capcompute/dispatcher"
)

// Provider adapts the dispatcher registry to aurora-capcompute's injected
// provider contract. The OpenAI cognition capability remains dispatchable but
// is intentionally hidden from the brain's operational tool list.
type Provider struct {
	registry *registry.Registry
	services registry.Services
}

func NewProvider(registrations ...registry.Registration) *Provider {
	return &Provider{registry: registry.New(registrations...)}
}

func (p *Provider) Normalize(name string, settings json.RawMessage) (json.RawMessage, error) {
	return p.registry.Normalize(name, settings)
}

func (p *Provider) NewDispatcher(
	ctx context.Context,
	_ aurora.RunContext,
	manifest aurora.Manifest,
) (dispatcher.Dispatcher[aurora.RunContext], error) {
	entries := make([]registry.Entry, 0, len(manifest.Capabilities))
	for _, capability := range manifest.Capabilities {
		entries = append(entries, registry.Entry{
			Name: capability.Name, Settings: capability.Settings,
		})
	}
	config, err := p.registry.Build(ctx, entries, p.services)
	if err != nil {
		return nil, err
	}
	config.Capabilities = visibleCapabilities(config.Capabilities)
	base := builtin.New[aurora.RunContext](config)
	return &guardedDispatcher{
		next: base, capabilities: append([]dispatcher.Capability(nil), config.Capabilities...),
	}, nil
}

func visibleCapabilities(all []dispatcher.Capability) []dispatcher.Capability {
	result := make([]dispatcher.Capability, 0, len(all))
	for _, capability := range all {
		if capability.Name != "openai.chat" {
			result = append(result, capability)
		}
	}
	return result
}

type guardedDispatcher struct {
	next         dispatcher.Dispatcher[aurora.RunContext]
	capabilities []dispatcher.Capability
}

func (d *guardedDispatcher) Capabilities() []dispatcher.Capability {
	return append([]dispatcher.Capability(nil), d.capabilities...)
}

func (d *guardedDispatcher) Dispatch(
	ctx context.Context,
	key aurora.RunContext,
	call dispatcher.Call,
) (dispatcher.Outcome, error) {
	if isKubernetesSecretCall(call) {
		return dispatcher.Failed("native Kubernetes Secret operations are disabled"), nil
	}
	return d.next.Dispatch(ctx, key, call)
}

func isKubernetesSecretCall(call dispatcher.Call) bool {
	if !strings.HasPrefix(call.Name, "k8s.") {
		return false
	}
	var payload map[string]any
	if json.Unmarshal(call.Args, &payload) != nil {
		return false
	}
	kind, _ := payload["kind"].(string)
	if call.Name == "k8s.apply" {
		if resource, ok := payload["resource"].(map[string]any); ok {
			kind, _ = resource["kind"].(string)
		}
	}
	return strings.EqualFold(strings.TrimSpace(kind), "secret")
}
