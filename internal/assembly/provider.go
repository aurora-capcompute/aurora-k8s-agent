// Package assembly wires this agent's concrete implementations into the
// implementation-neutral aurora-capcompute runtime: it adapts the dispatcher
// registry to the injected DispatcherProvider contract, supplies brain providers
// (OCI-backed or empty), and guards Secret operations at the dispatch level. It
// owns the choice of capabilities and brains this deployment exposes; the runtime
// mechanics stay in aurora-capcompute.
package assembly

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/aurora-capcompute/aurora-capcompute/aurora"
	"github.com/aurora-capcompute/aurora-dispatchers/builtin"
	"github.com/aurora-capcompute/aurora-dispatchers/registry"
	"github.com/aurora-capcompute/capcompute/dispatcher"
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

func (p *Provider) SetServices(services registry.Services) {
	p.services = services
}

func (p *Provider) Normalize(name string, settings json.RawMessage) (json.RawMessage, error) {
	return p.registry.Normalize(name, settings)
}

func (p *Provider) IsSubset(name string, parent, child json.RawMessage) error {
	return p.registry.IsSubset(name, parent, child)
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
	base := builtin.New[aurora.RunContext](config)
	return &guardedDispatcher{next: base}, nil
}

type guardedDispatcher struct {
	next dispatcher.Dispatcher[aurora.RunContext]
}

func (d *guardedDispatcher) Capabilities() []dispatcher.Capability {
	return d.next.Capabilities()
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
