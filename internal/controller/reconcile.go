// Package controller resolves the Aurora control-plane resources (Brain,
// FunctionInstance, Channel) into the agent's runtime configuration: which brain
// artifacts to load, and which validated (brain, capability-subset, channel)
// bindings to serve. Reconcile is a pure function over decoded specs so it can be
// unit-tested without a cluster; the informer wiring that feeds it lives
// alongside.
package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"aurora-capcompute/aurora"

	"aurora-k8s-agent/internal/apis/aurora/v1alpha1"
	"aurora-k8s-agent/internal/assembly"
	"aurora-k8s-agent/internal/binding"
	"aurora-k8s-agent/internal/brainspec"
	"aurora-k8s-agent/internal/oci"
)

// Named* pair a resource's name with its decoded spec.
type NamedBrain struct {
	Name string
	Spec v1alpha1.BrainSpec
}
type NamedChannel struct {
	Name string
	Spec v1alpha1.ChannelSpec
}
type NamedInstance struct {
	Name string
	Spec v1alpha1.FunctionInstanceSpec
}

// Inputs is the full set of control-plane resources to resolve.
type Inputs struct {
	Brains    []NamedBrain
	Channels  []NamedChannel
	Instances []NamedInstance
}

// SourceBinding is a validated function instance projected onto a source.
type SourceBinding struct {
	Source string
	// Name is the FunctionInstance name, so a binding is addressable (e.g. by a
	// web UI switching between the manifests bound to a channel).
	Name string
	binding.Resolved
}

// Resolved is the outcome of reconciliation: the brain artifacts to load, the
// bindings to serve, and the status to write back to each resource.
type Resolved struct {
	BrainRefs      []string
	Bindings       []SourceBinding
	BrainStatus    map[string]v1alpha1.BrainStatus
	ChannelStatus  map[string]v1alpha1.ChannelStatus
	InstanceStatus map[string]v1alpha1.FunctionInstanceStatus
}

type loadedBrain struct {
	decl   brainspec.Manifest
	ref    string
	digest string
}

// Reconcile resolves inputs into runtime config and per-resource status. It never
// errors: every failure is recorded as a not-ready status on the offending
// resource, so a single bad object cannot block the rest.
func Reconcile(ctx context.Context, in Inputs, puller oci.Puller, provider aurora.DispatcherProvider) Resolved {
	res := Resolved{
		BrainStatus:    make(map[string]v1alpha1.BrainStatus, len(in.Brains)),
		ChannelStatus:  make(map[string]v1alpha1.ChannelStatus, len(in.Channels)),
		InstanceStatus: make(map[string]v1alpha1.FunctionInstanceStatus, len(in.Instances)),
	}

	brains := make(map[string]loadedBrain, len(in.Brains))
	for _, b := range in.Brains {
		artifact, err := puller.Pull(ctx, b.Spec.Artifact)
		if err != nil {
			res.BrainStatus[b.Name] = v1alpha1.BrainStatus{Message: fmt.Sprintf("pull %s: %v", b.Spec.Artifact, err)}
			continue
		}
		brains[b.Name] = loadedBrain{decl: artifact.Manifest, ref: b.Spec.Artifact, digest: artifact.Digest}
		res.BrainStatus[b.Name] = v1alpha1.BrainStatus{
			Ready: true, Digest: artifact.Digest, BrainID: artifact.Manifest.ID,
			Capabilities: capabilityNames(artifact.Manifest),
		}
	}

	channels := make(map[string]v1alpha1.ChannelSpec, len(in.Channels))
	for _, c := range in.Channels {
		if err := validateChannel(c.Spec); err != nil {
			res.ChannelStatus[c.Name] = v1alpha1.ChannelStatus{Message: err.Error()}
			continue
		}
		channels[c.Name] = c.Spec
		res.ChannelStatus[c.Name] = v1alpha1.ChannelStatus{Ready: true}
	}

	refs := make(map[string]struct{})
	for _, fi := range in.Instances {
		brain, ok := brains[fi.Spec.BrainRef]
		if !ok {
			res.InstanceStatus[fi.Name] = notReady("brain %q is not ready", fi.Spec.BrainRef)
			continue
		}
		channel, ok := channels[fi.Spec.ChannelRef]
		if !ok {
			res.InstanceStatus[fi.Name] = notReady("channel %q is not ready", fi.Spec.ChannelRef)
			continue
		}
		manifest := buildManifest(fi.Spec, brain.decl.ID, brains)

		if err := assembly.ValidateGrant(brain.decl, manifest.Capabilities, provider); err != nil {
			res.InstanceStatus[fi.Name] = notReady("%v", err)
			continue
		}
		if badChild := validateChildGrants(fi.Spec, brains, provider); badChild != nil {
			res.InstanceStatus[fi.Name] = notReady("%v", badChild)
			continue
		}
		if err := assembly.ValidateChildrenSubset(manifest, provider); err != nil {
			res.InstanceStatus[fi.Name] = notReady("%v", err)
			continue
		}
		validated, err := aurora.ValidateManifest(manifest, provider)
		if err != nil {
			res.InstanceStatus[fi.Name] = notReady("%v", err)
			continue
		}
		if len(fi.Spec.Subjects.Users) == 0 || len(fi.Spec.Subjects.Scopes) == 0 {
			res.InstanceStatus[fi.Name] = notReady("subjects.users and subjects.scopes are required")
			continue
		}

		res.Bindings = append(res.Bindings, SourceBinding{
			Source: channel.Source,
			Name:   fi.Name,
			Resolved: binding.Resolved{
				Users:    append([]string(nil), fi.Spec.Subjects.Users...),
				Scopes:   append([]string(nil), fi.Spec.Subjects.Scopes...),
				Manifest: validated,
				Digest:   digest(validated),
			},
		})
		refs[brain.ref] = struct{}{}
		for _, child := range fi.Spec.Children {
			if cb, ok := brains[child.BrainRef]; ok {
				refs[cb.ref] = struct{}{}
			}
		}
		res.InstanceStatus[fi.Name] = v1alpha1.FunctionInstanceStatus{Ready: true}
	}

	res.BrainRefs = sortedKeys(refs)
	return res
}

func buildManifest(spec v1alpha1.FunctionInstanceSpec, brainID string, brains map[string]loadedBrain) aurora.Manifest {
	manifest := aurora.Manifest{
		Version:      aurora.ManifestVersion,
		Brain:        brainID,
		SystemPrompt: spec.SystemPrompt,
		Capabilities: toCapabilities(spec.Capabilities),
	}
	for _, child := range spec.Children {
		childBrain := child.BrainRef
		if b, ok := brains[child.BrainRef]; ok {
			childBrain = b.decl.ID
		}
		manifest.Children = append(manifest.Children, aurora.ChildManifest{
			Name:         child.Name,
			Brain:        childBrain,
			SystemPrompt: child.SystemPrompt,
			Capabilities: toCapabilities(child.Capabilities),
		})
	}
	return manifest
}

// validateChildGrants checks each child's capabilities against its own brain's
// declaration (in addition to the parent-subset check done elsewhere).
func validateChildGrants(spec v1alpha1.FunctionInstanceSpec, brains map[string]loadedBrain, provider aurora.DispatcherProvider) error {
	for _, child := range spec.Children {
		cb, ok := brains[child.BrainRef]
		if !ok {
			return fmt.Errorf("child %q brain %q is not ready", child.Name, child.BrainRef)
		}
		if err := assembly.ValidateGrant(cb.decl, toCapabilities(child.Capabilities), provider); err != nil {
			return fmt.Errorf("child %q: %w", child.Name, err)
		}
	}
	return nil
}

func toCapabilities(in []v1alpha1.Capability) []aurora.CapabilityConfig {
	out := make([]aurora.CapabilityConfig, len(in))
	for i, c := range in {
		out[i] = aurora.CapabilityConfig{Name: c.Name, Settings: c.Settings}
	}
	return out
}

func validateChannel(spec v1alpha1.ChannelSpec) error {
	switch spec.Source {
	case "telegram", "slack":
		if spec.SecretRef == "" {
			return fmt.Errorf("secretRef is required")
		}
	case "web":
		// The web channel is driven over HTTP and carries no transport secret.
	default:
		return fmt.Errorf("unsupported source %q (want telegram, slack, or web)", spec.Source)
	}
	return nil
}

// Digest returns the canonical content digest of a manifest, matching the digest
// recorded on a resolved binding. It lets callers group threads by the manifest
// that produced them.
func Digest(m aurora.Manifest) string { return digest(m) }

func capabilityNames(m brainspec.Manifest) []string {
	out := make([]string, len(m.Capabilities))
	for i, c := range m.Capabilities {
		out[i] = c.Name
	}
	return out
}

func notReady(format string, args ...any) v1alpha1.FunctionInstanceStatus {
	return v1alpha1.FunctionInstanceStatus{Message: fmt.Sprintf(format, args...)}
}

func digest(m aurora.Manifest) string {
	raw, _ := json.Marshal(m)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func sortedKeys(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
