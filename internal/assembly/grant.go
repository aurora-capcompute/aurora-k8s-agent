package assembly

import (
	"encoding/json"
	"fmt"

	"aurora-capcompute/aurora"

	"aurora-k8s-agent/internal/brainspec"
)

// ValidateGrant enforces the privilege boundary between a brain and a function
// instance: the capabilities the instance grants must be a valid subset of what
// the brain declares.
//
//   - every granted capability must be declared by the brain;
//   - every required (non-optional) declared capability must be granted;
//   - where the brain declares settings for a capability, the granted settings
//     must be within them (provider.IsSubset) — e.g. a narrower namespace set.
func ValidateGrant(decl brainspec.Manifest, granted []aurora.CapabilityConfig, provider aurora.DispatcherProvider) error {
	grantedNames := make(map[string]struct{}, len(granted))
	for _, cap := range granted {
		declared, ok := decl.Declared(cap.Name)
		if !ok {
			return fmt.Errorf("capability %q is not declared by brain %q", cap.Name, decl.ID)
		}
		if len(declared.Settings) > 0 {
			if err := provider.IsSubset(cap.Name, declared.Settings, cap.Settings); err != nil {
				return fmt.Errorf("capability %q exceeds brain %q declaration: %w", cap.Name, decl.ID, err)
			}
		}
		grantedNames[cap.Name] = struct{}{}
	}
	for _, required := range decl.Required() {
		if _, ok := grantedNames[required]; !ok {
			return fmt.Errorf("brain %q requires capability %q, which the instance does not grant", decl.ID, required)
		}
	}
	return nil
}

// BuildManifest projects a brain's declared tree plus a single flat grant into a
// runtime manifest, validating as it goes — the "binding satisfies the brain's
// interface" check, generalised over the whole tree:
//
//   - every name in allowed must be declared somewhere in the tree;
//   - at each node, every required (non-optional) declared capability must be in
//     allowed, and each granted capability's settings must be within that node's
//     declared settings (provider.IsSubset);
//   - each node carries exactly the intersection of its declared capabilities and
//     the grant, so the grant is a ceiling and per-node granularity stays with the
//     brain's declaration.
func BuildManifest(
	decl brainspec.Manifest,
	brainID, systemPrompt string,
	allowed []aurora.CapabilityConfig,
	provider aurora.DispatcherProvider,
) (aurora.Manifest, error) {
	grant := make(map[string]json.RawMessage, len(allowed))
	for _, cap := range allowed {
		grant[cap.Name] = cap.Settings
	}
	declared := decl.DeclaredNames()
	for name := range grant {
		if _, ok := declared[name]; !ok {
			return aurora.Manifest{}, fmt.Errorf("capability %q is not declared by brain %q", name, decl.ID)
		}
	}

	rootCaps, err := nodeCaps(decl.ID, decl.Capabilities, grant, provider)
	if err != nil {
		return aurora.Manifest{}, err
	}
	children, err := buildChildren(decl.Children, grant, provider)
	if err != nil {
		return aurora.Manifest{}, err
	}
	return aurora.Manifest{
		Version:      aurora.ManifestVersion,
		Brain:        brainID,
		SystemPrompt: systemPrompt,
		Capabilities: rootCaps,
		Children:     children,
	}, nil
}

func buildChildren(children []brainspec.Child, grant map[string]json.RawMessage, provider aurora.DispatcherProvider) ([]aurora.ChildManifest, error) {
	if len(children) == 0 {
		return nil, nil
	}
	out := make([]aurora.ChildManifest, 0, len(children))
	for _, ch := range children {
		caps, err := nodeCaps(ch.Name, ch.Capabilities, grant, provider)
		if err != nil {
			return nil, err
		}
		sub, err := buildChildren(ch.Children, grant, provider)
		if err != nil {
			return nil, err
		}
		out = append(out, aurora.ChildManifest{
			Name:         ch.Name,
			Brain:        ch.Brain,
			SystemPrompt: ch.SystemPrompt,
			Capabilities: caps,
			Children:     sub,
			OnFailure:    ch.OnFailure,
		})
	}
	return out, nil
}

// nodeCaps narrows one node's declared capabilities by the flat grant: every
// required capability must be granted, and granted settings must be within the
// node's declared settings.
func nodeCaps(node string, declared []brainspec.Capability, grant map[string]json.RawMessage, provider aurora.DispatcherProvider) ([]aurora.CapabilityConfig, error) {
	var out []aurora.CapabilityConfig
	for _, dc := range declared {
		settings, granted := grant[dc.Name]
		if !granted {
			if !dc.Optional {
				return nil, fmt.Errorf("node %q requires capability %q, which the binding does not grant", node, dc.Name)
			}
			continue
		}
		if len(dc.Settings) > 0 {
			if err := provider.IsSubset(dc.Name, dc.Settings, settings); err != nil {
				return nil, fmt.Errorf("node %q capability %q exceeds brain declaration: %w", node, dc.Name, err)
			}
		}
		out = append(out, aurora.CapabilityConfig{Name: dc.Name, Settings: settings})
	}
	return out, nil
}

// ValidateChildrenSubset enforces that every delegation child's capabilities are
// a subset of the parent's same-named capabilities. The upstream runtime
// validates children only in isolation; this closes that gap at projection time.
// A child may only use capabilities the parent holds, scoped no wider.
func ValidateChildrenSubset(manifest aurora.Manifest, provider aurora.DispatcherProvider) error {
	parent := make(map[string]json.RawMessage, len(manifest.Capabilities))
	for _, cap := range manifest.Capabilities {
		parent[cap.Name] = cap.Settings
	}
	for _, child := range manifest.Children {
		if err := validateChildSubset(child, parent, provider); err != nil {
			return err
		}
	}
	return nil
}

func validateChildSubset(child aurora.ChildManifest, parent map[string]json.RawMessage, provider aurora.DispatcherProvider) error {
	own := make(map[string]json.RawMessage, len(child.Capabilities))
	for _, cap := range child.Capabilities {
		parentSettings, ok := parent[cap.Name]
		if !ok {
			return fmt.Errorf("child %q capability %q is not held by its parent", child.Name, cap.Name)
		}
		if err := provider.IsSubset(cap.Name, parentSettings, cap.Settings); err != nil {
			return fmt.Errorf("child %q capability %q exceeds parent: %w", child.Name, cap.Name, err)
		}
		own[cap.Name] = cap.Settings
	}
	for _, grandchild := range child.Children {
		if err := validateChildSubset(grandchild, own, provider); err != nil {
			return err
		}
	}
	return nil
}

// ValidateManifest validates a function-instance manifest against the brain it
// names: the brain must be loaded, the grant must be a valid subset of the
// brain's declaration, and the delegation tree must respect parent scopes.
func (p *OCIBrainProvider) ValidateManifest(manifest aurora.Manifest, provider aurora.DispatcherProvider) error {
	brainID := manifest.Brain
	if brainID == "" {
		brainID = p.defaultID
	}
	decl, ok := p.specs[brainID]
	if !ok {
		return fmt.Errorf("brain %q is not loaded", brainID)
	}
	if err := ValidateGrant(decl, manifest.Capabilities, provider); err != nil {
		return err
	}
	return ValidateChildrenSubset(manifest, provider)
}
