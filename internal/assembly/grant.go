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
