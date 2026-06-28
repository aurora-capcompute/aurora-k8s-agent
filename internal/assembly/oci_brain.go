package assembly

import (
	"context"
	"errors"
	"fmt"

	"github.com/aurora-capcompute/aurora-capcompute/aurora"

	"github.com/aurora-capcompute/aurora-k8s-agent/internal/oci"
)

// OCIBrainProvider loads brains from OCI artifacts at startup and serves them
// through the aurora.BrainProvider interface. Brain IDs are namespaced as
// artifactDigest/brainName to prevent collisions across different artifacts.
type OCIBrainProvider struct {
	defaultID string
	brains    []aurora.BrainSource
}

// NewOCIBrainProvider pulls every reference and registers all WASM binaries
// from each artifact. Brain IDs are formatted as artifactDigest/brainName.
// The default brain ID is the main brain of the first pulled artifact.
func NewOCIBrainProvider(
	ctx context.Context,
	refs []string,
	puller oci.Puller,
) (*OCIBrainProvider, error) {
	if len(refs) == 0 {
		return nil, errors.New("no brain references configured")
	}
	p := &OCIBrainProvider{}
	registered := make(map[string]struct{})
	for _, ref := range refs {
		artifact, err := puller.Pull(ctx, ref)
		if err != nil {
			return nil, fmt.Errorf("pull brain %q: %w", ref, err)
		}
		for name, wasm := range artifact.Brains {
			id := artifact.Digest + "/" + name
			if _, dup := registered[id]; dup {
				return nil, fmt.Errorf("brain id %q is provided by more than one artifact", id)
			}
			registered[id] = struct{}{}
			p.brains = append(p.brains, aurora.BrainSource{ID: id, Wasm: wasm})
			if p.defaultID == "" && name == artifact.Main {
				p.defaultID = id
			}
		}
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
