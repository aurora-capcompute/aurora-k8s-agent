// Package controller resolves the Aurora control-plane resources (Brain, the
// typed channels SlackChannel/TelegramChannel/WebChannel, and ChannelBinding)
// into the agent's runtime configuration: which brain artifacts to load, and
// which validated bindings to serve. Reconcile is a pure function over decoded
// specs so it can be unit-tested without a cluster; the informer wiring that
// feeds it lives alongside.
//
// The model: a Brain artifact *exposes an interface* (the capabilities its whole
// tree requires), a typed channel carries the transport plus its native subjects,
// and a ChannelBinding *satisfies* the interface with a single flat grant and
// wires the brain to the channel — validation happens here.
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
type NamedSlackChannel struct {
	Name string
	Spec v1alpha1.SlackChannelSpec
}
type NamedTelegramChannel struct {
	Name string
	Spec v1alpha1.TelegramChannelSpec
}
type NamedWebChannel struct {
	Name string
	Spec v1alpha1.WebChannelSpec
}
type NamedBinding struct {
	Name string
	Spec v1alpha1.ChannelBindingSpec
}

// Inputs is the full set of control-plane resources to resolve.
type Inputs struct {
	Brains           []NamedBrain
	SlackChannels    []NamedSlackChannel
	TelegramChannels []NamedTelegramChannel
	WebChannels      []NamedWebChannel
	Bindings         []NamedBinding
}

// SourceBinding is a validated ChannelBinding projected onto a transport source.
type SourceBinding struct {
	Source string
	// Name is the ChannelBinding name, so a binding is addressable (e.g. by a web
	// UI switching between the manifests bound to a channel).
	Name string
	binding.Resolved
}

// Resolved is the outcome of reconciliation: the brain artifacts to load, the
// bindings to serve, and the status to write back to each resource. Channel and
// binding statuses are keyed by name (channels additionally namespaced by kind
// via ChannelKey, since names can repeat across the typed channel kinds).
type Resolved struct {
	BrainRefs     []string
	Brains        []ResolvedBrain
	Bindings      []SourceBinding
	Channels      []ResolvedChannel
	BrainStatus   map[string]v1alpha1.BrainStatus
	ChannelStatus map[string]v1alpha1.ChannelStatus
	BindingStatus map[string]v1alpha1.ChannelBindingStatus
}

// ResolvedChannel groups a ready typed channel with the bindings that target it,
// so a channel supervisor can construct one live bridge per channel CRD. Secrets
// carries the channel's unresolved credential sources (keyed e.g. "botToken",
// "appToken"); the supervisor holds the key and resolves them.
type ResolvedChannel struct {
	Kind     string
	Name     string
	Source   string
	Secrets  map[string]v1alpha1.SecretSource
	Bindings []binding.Resolved
}

// ChannelKey is the status-map key for a typed channel.
func ChannelKey(kind, name string) string { return kind + "/" + name }

type loadedBrain struct {
	decl   brainspec.Manifest
	ref    string
	digest string
	wasm   []byte
}

// ResolvedBrain is a ready brain artifact's runnable payload: the declared brain
// id the runtime registers it under, its wasm, and its content digest. The agent
// feeds these to runtime.SetBrains so Brain CRDs hot-load into a running runtime.
type ResolvedBrain struct {
	ID     string
	Digest string
	Wasm   []byte
}

// resolvedChannel is a typed channel normalised to a transport source plus its
// subjects and credential sources, after validation. bindings accumulates the
// bindings that successfully resolve against it.
type resolvedChannel struct {
	kind     string
	name     string
	source   string
	users    []string
	scopes   []string
	secrets  map[string]v1alpha1.SecretSource
	bindings []binding.Resolved
}

// Reconcile resolves inputs into runtime config and per-resource status. It never
// errors: every failure is recorded as a not-ready status on the offending
// resource, so a single bad object cannot block the rest.
func Reconcile(ctx context.Context, in Inputs, puller oci.Puller, provider aurora.DispatcherProvider) Resolved {
	res := Resolved{
		BrainStatus:   make(map[string]v1alpha1.BrainStatus, len(in.Brains)),
		ChannelStatus: make(map[string]v1alpha1.ChannelStatus),
		BindingStatus: make(map[string]v1alpha1.ChannelBindingStatus, len(in.Bindings)),
	}

	brains := make(map[string]loadedBrain, len(in.Brains))
	for _, b := range in.Brains {
		artifact, err := puller.Pull(ctx, b.Spec.Artifact)
		if err != nil {
			res.BrainStatus[b.Name] = v1alpha1.BrainStatus{Message: fmt.Sprintf("pull %s: %v", b.Spec.Artifact, err)}
			continue
		}
		brains[b.Name] = loadedBrain{decl: artifact.Manifest, ref: b.Spec.Artifact, digest: artifact.Digest, wasm: artifact.Wasm}
		res.BrainStatus[b.Name] = v1alpha1.BrainStatus{
			Ready: true, Digest: artifact.Digest, BrainID: artifact.Manifest.ID,
			Capabilities: capabilityNames(artifact.Manifest),
		}
	}

	channels := make(map[string]*resolvedChannel)
	resolveChannels(in, channels, res.ChannelStatus)

	refs := make(map[string]struct{})
	for _, bnd := range in.Bindings {
		brain, ok := brains[bnd.Spec.BrainRef]
		if !ok {
			res.BindingStatus[bnd.Name] = bindingNotReady("brain %q is not ready", bnd.Spec.BrainRef)
			continue
		}
		channel, ok := channels[ChannelKey(bnd.Spec.ChannelRef.Kind, bnd.Spec.ChannelRef.Name)]
		if !ok {
			res.BindingStatus[bnd.Name] = bindingNotReady("channel %s/%s is not ready",
				bnd.Spec.ChannelRef.Kind, bnd.Spec.ChannelRef.Name)
			continue
		}

		manifest, err := assembly.BuildManifest(brain.decl, brain.decl.ID, bnd.Spec.SystemPrompt, toCapabilities(bnd.Spec.Allowed), provider)
		if err != nil {
			res.BindingStatus[bnd.Name] = bindingNotReady("%v", err)
			continue
		}
		if err := assembly.ValidateChildrenSubset(manifest, provider); err != nil {
			res.BindingStatus[bnd.Name] = bindingNotReady("%v", err)
			continue
		}
		validated, err := aurora.ValidateManifest(manifest, provider)
		if err != nil {
			res.BindingStatus[bnd.Name] = bindingNotReady("%v", err)
			continue
		}

		resolved := binding.Resolved{
			Users:    append([]string(nil), channel.users...),
			Scopes:   append([]string(nil), channel.scopes...),
			Manifest: validated,
			Digest:   digest(validated),
		}
		res.Bindings = append(res.Bindings, SourceBinding{Source: channel.source, Name: bnd.Name, Resolved: resolved})
		channel.bindings = append(channel.bindings, resolved)
		refs[brain.ref] = struct{}{}
		res.BindingStatus[bnd.Name] = v1alpha1.ChannelBindingStatus{Ready: true}
	}

	res.Channels = collectChannels(channels)
	res.Brains = collectBrains(brains)
	res.BrainRefs = sortedKeys(refs)
	return res
}

// collectBrains projects every ready brain into a runnable artifact for the
// runtime, keyed by the brain's declared id (which is what manifests reference).
// If two Brain resources declare the same id, the first by id order wins; the
// result is sorted for a stable SetBrains apply.
func collectBrains(brains map[string]loadedBrain) []ResolvedBrain {
	byID := make(map[string]ResolvedBrain, len(brains))
	for _, b := range brains {
		if len(b.wasm) == 0 {
			continue
		}
		if _, dup := byID[b.decl.ID]; dup {
			continue
		}
		byID[b.decl.ID] = ResolvedBrain{ID: b.decl.ID, Digest: b.digest, Wasm: b.wasm}
	}
	out := make([]ResolvedBrain, 0, len(byID))
	for _, rb := range byID {
		out = append(out, rb)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// collectChannels turns the ready-channel registry into a stable, exported slice.
func collectChannels(channels map[string]*resolvedChannel) []ResolvedChannel {
	out := make([]ResolvedChannel, 0, len(channels))
	for _, c := range channels {
		out = append(out, ResolvedChannel{
			Kind:     c.kind,
			Name:     c.name,
			Source:   c.source,
			Secrets:  c.secrets,
			Bindings: c.bindings,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// resolveChannels validates each typed channel and records it in the registry
// (keyed by ChannelKey) and its status. Slack/Telegram require credentials and
// non-empty subjects; Web carries no secret and may omit subjects.
func resolveChannels(in Inputs, out map[string]*resolvedChannel, status map[string]v1alpha1.ChannelStatus) {
	for _, c := range in.SlackChannels {
		key := ChannelKey(v1alpha1.KindSlackChannel, c.Name)
		if err := validateSecret("appToken", c.Spec.AppToken); err != nil {
			status[key] = v1alpha1.ChannelStatus{Message: err.Error()}
			continue
		}
		if err := validateSecret("botToken", c.Spec.BotToken); err != nil {
			status[key] = v1alpha1.ChannelStatus{Message: err.Error()}
			continue
		}
		if err := validateSubjects(c.Spec.Users, c.Spec.Scopes); err != nil {
			status[key] = v1alpha1.ChannelStatus{Message: err.Error()}
			continue
		}
		out[key] = &resolvedChannel{
			kind: v1alpha1.KindSlackChannel, name: c.Name, source: "slack",
			users: c.Spec.Users, scopes: c.Spec.Scopes,
			secrets: map[string]v1alpha1.SecretSource{"appToken": c.Spec.AppToken, "botToken": c.Spec.BotToken},
		}
		status[key] = v1alpha1.ChannelStatus{Ready: true}
	}
	for _, c := range in.TelegramChannels {
		key := ChannelKey(v1alpha1.KindTelegramChannel, c.Name)
		if err := validateSecret("botToken", c.Spec.BotToken); err != nil {
			status[key] = v1alpha1.ChannelStatus{Message: err.Error()}
			continue
		}
		if err := validateSubjects(c.Spec.Users, c.Spec.Scopes); err != nil {
			status[key] = v1alpha1.ChannelStatus{Message: err.Error()}
			continue
		}
		out[key] = &resolvedChannel{
			kind: v1alpha1.KindTelegramChannel, name: c.Name, source: "telegram",
			users: c.Spec.Users, scopes: c.Spec.Scopes,
			secrets: map[string]v1alpha1.SecretSource{"botToken": c.Spec.BotToken},
		}
		status[key] = v1alpha1.ChannelStatus{Ready: true}
	}
	for _, c := range in.WebChannels {
		key := ChannelKey(v1alpha1.KindWebChannel, c.Name)
		out[key] = &resolvedChannel{
			kind: v1alpha1.KindWebChannel, name: c.Name, source: "web",
			users: c.Spec.Users, scopes: c.Spec.Scopes,
		}
		status[key] = v1alpha1.ChannelStatus{Ready: true}
	}
}

func validateSecret(field string, src v1alpha1.SecretSource) error {
	switch src.Type {
	case v1alpha1.SecretInPlaceEncrypted:
		if src.Ciphertext == "" {
			return fmt.Errorf("%s.ciphertext is required for inPlaceEncrypted", field)
		}
	case v1alpha1.SecretStorage:
		if src.Ref == nil || src.Ref.Name == "" || src.Ref.Key == "" {
			return fmt.Errorf("%s.ref{name,key} is required for secretStorage", field)
		}
	case "":
		return fmt.Errorf("%s.type is required", field)
	default:
		return fmt.Errorf("%s.type %q is unsupported", field, src.Type)
	}
	return nil
}

func validateSubjects(users, scopes []string) error {
	if len(users) == 0 || len(scopes) == 0 {
		return fmt.Errorf("users and scopes are required")
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

// Digest returns the canonical content digest of a manifest, matching the digest
// recorded on a resolved binding. It lets callers group threads by the manifest
// that produced them.
func Digest(m aurora.Manifest) string { return digest(m) }

func capabilityNames(m brainspec.Manifest) []string {
	names := m.DeclaredNames()
	out := make([]string, 0, len(names))
	for name := range names {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func bindingNotReady(format string, args ...any) v1alpha1.ChannelBindingStatus {
	return v1alpha1.ChannelBindingStatus{Message: fmt.Sprintf(format, args...)}
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
