package assembly

import (
	"context"
	"errors"
	"fmt"

	"aurora-capcompute/aurora"

	"aurora-k8s-agent/internal/brainspec"
	"aurora-k8s-agent/internal/oci"
)

// OCIBrainProvider loads brains from OCI artifacts at startup and serves them
// through the aurora.BrainProvider interface, exactly like the embedded provider.
// It also retains each brain's declared capability set for subset validation.
type OCIBrainProvider struct {
	defaultID string
	brains    []aurora.BrainSource
	specs     map[string]brainspec.Manifest
}

// NewOCIBrainProvider pulls every reference and indexes the brains by their
// declared id. defaultID selects the brain used when a manifest omits one; empty
// means the first reference. It is an error for two artifacts to share an id or
// for defaultID to name a brain that was not pulled.
func NewOCIBrainProvider(
	ctx context.Context,
	refs []string,
	defaultID string,
	puller oci.Puller,
) (*OCIBrainProvider, error) {
	if len(refs) == 0 {
		return nil, errors.New("no brain references configured")
	}
	p := &OCIBrainProvider{specs: make(map[string]brainspec.Manifest, len(refs))}
	for _, ref := range refs {
		artifact, err := puller.Pull(ctx, ref)
		if err != nil {
			return nil, fmt.Errorf("pull brain %q: %w", ref, err)
		}
		id := artifact.Manifest.ID
		if _, dup := p.specs[id]; dup {
			return nil, fmt.Errorf("brain id %q is provided by more than one artifact", id)
		}
		p.specs[id] = artifact.Manifest
		p.brains = append(p.brains, aurora.BrainSource{ID: id, Wasm: artifact.Wasm})
	}
	p.defaultID = defaultID
	if p.defaultID == "" {
		p.defaultID = p.brains[0].ID
	}
	if _, ok := p.specs[p.defaultID]; !ok {
		return nil, fmt.Errorf("default brain %q is not among the configured brains", p.defaultID)
	}
	return p, nil
}

// DefaultID implements aurora.BrainProvider.
func (p *OCIBrainProvider) DefaultID() string { return p.defaultID }

// List implements aurora.BrainProvider, returning defensive copies.
func (p *OCIBrainProvider) List(context.Context) ([]aurora.BrainSource, error) {
	out := make([]aurora.BrainSource, len(p.brains))
	for i, b := range p.brains {
		out[i] = aurora.BrainSource{ID: b.ID, Wasm: append([]byte(nil), b.Wasm...)}
	}
	return out, nil
}

// Declarations returns each brain's declared capability manifest, for subset
// validation of function instances (Phase 2).
func (p *OCIBrainProvider) Declarations() map[string]brainspec.Manifest {
	out := make(map[string]brainspec.Manifest, len(p.specs))
	for id, spec := range p.specs {
		out[id] = spec
	}
	return out
}
