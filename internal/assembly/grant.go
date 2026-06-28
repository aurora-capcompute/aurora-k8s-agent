package assembly

import (
	"fmt"

	"github.com/aurora-capcompute/aurora-capcompute/aurora"

	"github.com/aurora-capcompute/aurora-k8s-agent/internal/apis/aurora/v1alpha1"
)

// BuildManifest constructs an aurora.Manifest from the ChannelBinding's
// capability and delegation tree. provider.Normalize is called for each
// capability to fill defaults and validate settings; optional capabilities
// whose Normalize returns an error are silently skipped, required ones fail
// the manifest. Children are wired to the same artifact via artifactDigest.
func BuildManifest(
	brainID, systemPrompt, bindingName, artifactDigest string,
	caps []v1alpha1.Capability,
	children []v1alpha1.ChildSpec,
	provider aurora.DispatcherProvider,
) (aurora.Manifest, error) {
	rootCaps, err := nodeCaps(caps, provider)
	if err != nil {
		return aurora.Manifest{}, err
	}
	ch, err := buildChildren(children, bindingName, artifactDigest, provider)
	if err != nil {
		return aurora.Manifest{}, err
	}
	return aurora.Manifest{
		Version:      aurora.ManifestVersion,
		Brain:        brainID,
		BindingRef:   bindingName,
		SystemPrompt: systemPrompt,
		Capabilities: rootCaps,
		Children:     ch,
	}, nil
}

func buildChildren(children []v1alpha1.ChildSpec, bindingName, artifactDigest string, provider aurora.DispatcherProvider) ([]aurora.ChildManifest, error) {
	if len(children) == 0 {
		return nil, nil
	}
	out := make([]aurora.ChildManifest, 0, len(children))
	for _, ch := range children {
		caps, err := nodeCaps(ch.Capabilities, provider)
		if err != nil {
			return nil, fmt.Errorf("child %q: %w", ch.Name, err)
		}
		sub, err := buildChildren(ch.Children, bindingName, artifactDigest, provider)
		if err != nil {
			return nil, err
		}
		out = append(out, aurora.ChildManifest{
			Name:         ch.Name,
			Brain:        artifactDigest + "/" + ch.Brain,
			BindingRef:   bindingName,
			SystemPrompt: ch.SystemPrompt,
			Capabilities: caps,
			Children:     sub,
			OnFailure:    ch.OnFailure,
		})
	}
	return out, nil
}

// nodeCaps resolves one node's capabilities via provider.Normalize. Optional
// capabilities that Normalize rejects are skipped; required ones fail.
func nodeCaps(caps []v1alpha1.Capability, provider aurora.DispatcherProvider) ([]aurora.CapabilityConfig, error) {
	var out []aurora.CapabilityConfig
	for _, c := range caps {
		settings := v1alpha1.ResolveLiterals(c.Settings)
		normalized, err := provider.Normalize(c.Name, settings)
		if err != nil {
			if c.Optional {
				continue
			}
			return nil, fmt.Errorf("capability %q: %w", c.Name, err)
		}
		out = append(out, aurora.CapabilityConfig{Name: c.Name, Settings: normalized})
	}
	return out, nil
}
