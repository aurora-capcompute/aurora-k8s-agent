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
	"fmt"
	"strings"
	"sync"

	"github.com/aurora-capcompute/aurora-capcompute/aurora"
	"github.com/aurora-capcompute/aurora-dispatchers/builtin"
	"github.com/aurora-capcompute/aurora-dispatchers/registry"
	"github.com/aurora-capcompute/capcompute/dispatcher"

	"github.com/aurora-capcompute/aurora-k8s-agent/internal/apis/aurora/v1alpha1"
	"github.com/aurora-capcompute/aurora-k8s-agent/internal/binding"
	"github.com/aurora-capcompute/aurora-k8s-agent/internal/secrets"
)

// bindingEntry holds fully-resolved settings per capability for one binding,
// keyed by capability name. Values are plain JSON ready for registry.Entry.Settings.
type bindingEntry struct {
	caps map[string]json.RawMessage
}

// Provider adapts the dispatcher registry to aurora-capcompute's injected
// provider contract. The OpenAI cognition capability remains dispatchable but
// is intentionally hidden from the brain's operational tool list.
type Provider struct {
	registry *registry.Registry
	services registry.Services
	resolver secrets.Resolver
	store    sync.Map // bindingRef string → bindingEntry
}

func NewProvider(registrations ...registry.Registration) *Provider {
	return &Provider{registry: registry.New(registrations...)}
}

func (p *Provider) SetServices(services registry.Services) {
	p.services = services
}

// SetResolver configures the secret resolver used by Warmup. Must be called
// before the first Warmup call; safe to call concurrently otherwise.
func (p *Provider) SetResolver(r secrets.Resolver) {
	p.resolver = r
}

// Warmup resolves all ADT capability settings for each binding and caches them
// under the binding's ref. Called once at channel start and again on hot-swap
// when bindings change. Fails fast if any secret cannot be resolved.
func (p *Provider) Warmup(bindings []binding.Resolved) error {
	for _, b := range bindings {
		if b.BindingRef == "" {
			continue // file-based binding — no warmup needed
		}
		caps := make(map[string]json.RawMessage, len(b.CapabilitySettings))
		for capName, settings := range b.CapabilitySettings {
			resolved, err := p.resolveSettings(capName, settings)
			if err != nil {
				return fmt.Errorf("binding %q capability %q: %w", b.BindingRef, capName, err)
			}
			caps[capName] = resolved
		}
		p.store.Store(b.BindingRef, bindingEntry{caps: caps})
	}
	return nil
}

// resolveSettings converts each SettingValue in the map to its plain JSON value.
func (p *Provider) resolveSettings(capName string, settings map[string]v1alpha1.SettingValue) (json.RawMessage, error) {
	out := make(map[string]json.RawMessage, len(settings))
	for k, sv := range settings {
		val, err := p.resolveOne(sv)
		if err != nil {
			return nil, fmt.Errorf("setting %q: %w", k, err)
		}
		out[k] = val
	}
	return json.Marshal(out)
}

func (p *Provider) resolveOne(sv v1alpha1.SettingValue) (json.RawMessage, error) {
	switch sv.Type {
	case v1alpha1.SettingLiteral:
		return sv.Value, nil
	case v1alpha1.SecretInPlaceEncrypted, v1alpha1.SecretStorage:
		if p.resolver == nil {
			return nil, fmt.Errorf("no secret resolver configured")
		}
		src := v1alpha1.SecretSource{Type: sv.Type, Ciphertext: sv.Ciphertext, Ref: sv.Ref}
		raw, err := p.resolver.Resolve(src)
		if err != nil {
			return nil, err
		}
		// Wrap the raw bytes as a JSON string so the setting is valid JSON.
		return json.Marshal(string(raw))
	default:
		return nil, fmt.Errorf("unknown type %q", sv.Type)
	}
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
	var entries []registry.Entry
	if manifest.BindingRef != "" {
		val, ok := p.store.Load(manifest.BindingRef)
		if !ok {
			return nil, fmt.Errorf("no warmup entry for binding %q: call Warmup before dispatching", manifest.BindingRef)
		}
		entry := val.(bindingEntry)
		entries = make([]registry.Entry, 0, len(manifest.Capabilities))
		for _, capability := range manifest.Capabilities {
			settings := entry.caps[capability.Name]
			entries = append(entries, registry.Entry{Name: capability.Name, Settings: settings})
		}
	} else {
		// File-based binding: manifest carries plain-JSON settings directly.
		entries = make([]registry.Entry, 0, len(manifest.Capabilities))
		for _, capability := range manifest.Capabilities {
			entries = append(entries, registry.Entry{
				Name: capability.Name, Settings: capability.Settings,
			})
		}
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
	auth dispatcher.Authorization,
) (dispatcher.Outcome, error) {
	if isKubernetesSecretCall(call) {
		return dispatcher.Fail("native Kubernetes Secret operations are disabled"), nil
	}
	return d.next.Dispatch(ctx, key, call, auth)
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
