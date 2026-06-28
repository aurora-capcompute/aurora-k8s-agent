// Package controller resolves the Aurora control-plane resources (Brain, the
// typed channels SlackChannel/TelegramChannel/WebChannel, and ChannelBinding)
// into the agent's runtime configuration: which brain artifacts to load, and
// which validated bindings to serve. Reconcile is a pure function over decoded
// specs so it can be unit-tested without a cluster; the informer wiring that
// feeds it lives alongside.
//
// The model: a Brain artifact bundles named WASM binaries; a typed channel
// carries the transport plus its native subjects; a ChannelBinding declares
// the full capability tree (capabilities + children) and wires the brain to
// one or more channels — validation happens here.
package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/aurora-capcompute/aurora-capcompute/aurora"

	"github.com/aurora-capcompute/aurora-k8s-agent/internal/apis/aurora/v1alpha1"
	"github.com/aurora-capcompute/aurora-k8s-agent/internal/assembly"
	"github.com/aurora-capcompute/aurora-k8s-agent/internal/binding"
	"github.com/aurora-capcompute/aurora-k8s-agent/internal/oci"
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
// "appToken"); the supervisor holds the key and resolves them. Users carries
// web-channel login credentials (unresolved); webchannel.Channel.Apply resolves
// them with the same key.
type ResolvedChannel struct {
	Kind     string
	Name     string
	Source   string
	Secrets  map[string]v1alpha1.SecretSource
	Users    []v1alpha1.WebChannelUser // web channels only
	Bindings []binding.Resolved
}

// ChannelKey is the status-map key for a typed channel.
func ChannelKey(kind, name string) string { return kind + "/" + name }

type loadedBrain struct {
	brains map[string][]byte // brain name → wasm bytes
	main   string
	digest string
	ref    string
}

// ResolvedBrain is a ready brain artifact's runnable payload: the namespaced
// brain id (digest/name), its wasm, and its content digest.
type ResolvedBrain struct {
	ID     string
	Digest string
	Wasm   []byte
}

// resolvedChannel is a typed channel normalised to a transport source plus its
// subjects and credential sources, after validation.
type resolvedChannel struct {
	kind     string
	name     string
	source   string
	users    []string                  // Telegram/Slack: access-control user IDs
	webUsers []v1alpha1.WebChannelUser // Web: login credentials (unresolved)
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
		mainID := artifact.Digest + "/" + artifact.Main
		brains[b.Name] = loadedBrain{brains: artifact.Brains, main: artifact.Main, digest: artifact.Digest, ref: b.Spec.Artifact}
		res.BrainStatus[b.Name] = v1alpha1.BrainStatus{Ready: true, Digest: artifact.Digest, BrainID: mainID}
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

		if err := validateChildBrains(bnd.Spec.Children, brain.brains); err != nil {
			res.BindingStatus[bnd.Name] = bindingNotReady("%v", err)
			continue
		}

		if err := validateAllCapabilitySettings(bnd.Spec.Capabilities, bnd.Spec.Children); err != nil {
			res.BindingStatus[bnd.Name] = bindingNotReady("%v", err)
			continue
		}

		systemPrompt, err := resolveSystemPrompt(bnd.Spec.SystemPrompt)
		if err != nil {
			res.BindingStatus[bnd.Name] = bindingNotReady("%v", err)
			continue
		}

		rootBrainID := brain.digest + "/" + brain.main
		manifest, err := assembly.BuildManifest(rootBrainID, systemPrompt, bnd.Name, brain.digest,
			bnd.Spec.Capabilities, bnd.Spec.Children, provider)
		if err != nil {
			res.BindingStatus[bnd.Name] = bindingNotReady("%v", err)
			continue
		}

		// Resolve all channels before committing any SourceBindings.
		if len(bnd.Spec.Channels) == 0 {
			res.BindingStatus[bnd.Name] = bindingNotReady("no channels configured")
			continue
		}
		var resolvedChans []*resolvedChannel
		failed := false
		for _, cref := range bnd.Spec.Channels {
			ch, chOK := channels[ChannelKey(cref.Kind, cref.Name)]
			if !chOK {
				res.BindingStatus[bnd.Name] = bindingNotReady("channel %s/%s is not ready", cref.Kind, cref.Name)
				failed = true
				break
			}
			resolvedChans = append(resolvedChans, ch)
		}
		if failed {
			continue
		}

		capSettings := collectCapabilitySettings(bnd.Spec.Capabilities)
		md := digest(manifest)

		for _, ch := range resolvedChans {
			resolved := binding.Resolved{
				Users:              append([]string(nil), ch.users...),
				Scopes:             append([]string(nil), ch.scopes...),
				Manifest:           manifest,
				Digest:             md,
				CapabilitySettings: capSettings,
				BindingRef:         bnd.Name,
			}
			res.Bindings = append(res.Bindings, SourceBinding{Source: ch.source, Name: bnd.Name, Resolved: resolved})
			ch.bindings = append(ch.bindings, resolved)
		}
		refs[brain.ref] = struct{}{}
		res.BindingStatus[bnd.Name] = v1alpha1.ChannelBindingStatus{Ready: true}
	}

	res.Channels = collectChannels(channels)
	res.Brains = collectBrains(brains)
	res.BrainRefs = sortedKeys(refs)
	return res
}

// collectBrains projects every ready brain into a runnable artifact for the
// runtime. Brain IDs are digest/name; the result is sorted for a stable apply.
func collectBrains(brains map[string]loadedBrain) []ResolvedBrain {
	byID := make(map[string]ResolvedBrain)
	for _, b := range brains {
		for name, wasm := range b.brains {
			id := b.digest + "/" + name
			if _, dup := byID[id]; dup {
				continue
			}
			byID[id] = ResolvedBrain{ID: id, Digest: b.digest, Wasm: wasm}
		}
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
			Users:    c.webUsers,
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
		var chSecrets map[string]v1alpha1.SecretSource
		if c.Spec.Token != nil {
			if err := validateSecret("token", *c.Spec.Token); err != nil {
				status[key] = v1alpha1.ChannelStatus{Message: fmt.Sprintf("invalid token: %v", err)}
				continue
			}
			chSecrets = map[string]v1alpha1.SecretSource{"token": *c.Spec.Token}
		}
		failed := false
		for i, u := range c.Spec.Users {
			if err := validateSecret(fmt.Sprintf("users[%d].password", i), u.Password); err != nil {
				status[key] = v1alpha1.ChannelStatus{Message: err.Error()}
				failed = true
				break
			}
		}
		if failed {
			continue
		}
		out[key] = &resolvedChannel{
			kind: v1alpha1.KindWebChannel, name: c.Name, source: "web",
			webUsers: c.Spec.Users, scopes: c.Spec.Scopes,
			secrets: chSecrets,
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

// validateCapabilitySettings checks ADT structure for each setting value:
// type is known, and required variant fields are present.
func validateCapabilitySettings(capName string, settings map[string]v1alpha1.SettingValue) error {
	for k, sv := range settings {
		switch sv.Type {
		case v1alpha1.SettingLiteral:
			if len(sv.Value) == 0 {
				return fmt.Errorf("capability %q setting %q: literal value is required", capName, k)
			}
		case v1alpha1.SecretInPlaceEncrypted:
			if sv.Ciphertext == "" {
				return fmt.Errorf("capability %q setting %q: ciphertext is required for inPlaceEncrypted", capName, k)
			}
		case v1alpha1.SecretStorage:
			if sv.Ref == nil || sv.Ref.Name == "" || sv.Ref.Key == "" {
				return fmt.Errorf("capability %q setting %q: ref.{name,key} are required for secretStorage", capName, k)
			}
		case "":
			return fmt.Errorf("capability %q setting %q: type is required", capName, k)
		default:
			return fmt.Errorf("capability %q setting %q: unknown type %q", capName, k, sv.Type)
		}
	}
	return nil
}

// validateAllCapabilitySettings recursively validates ADT structure for
// capabilities at the root node and all children.
func validateAllCapabilitySettings(caps []v1alpha1.Capability, children []v1alpha1.ChildSpec) error {
	for _, cap := range caps {
		if err := validateCapabilitySettings(cap.Name, cap.Settings); err != nil {
			return err
		}
	}
	for _, ch := range children {
		if err := validateAllCapabilitySettings(ch.Capabilities, ch.Children); err != nil {
			return err
		}
	}
	return nil
}

// validateChildBrains checks that every ChildSpec.Brain names a WASM that is
// bundled in the artifact. Typos or missing bundles surface at reconcile time
// rather than as confusing runtime delegation failures.
func validateChildBrains(children []v1alpha1.ChildSpec, available map[string][]byte) error {
	for _, ch := range children {
		if _, ok := available[ch.Brain]; !ok {
			return fmt.Errorf("child %q references brain %q which is not bundled in the artifact", ch.Name, ch.Brain)
		}
		if err := validateChildBrains(ch.Children, available); err != nil {
			return err
		}
	}
	return nil
}

// resolveSystemPrompt extracts the system prompt string from its SettingValue.
// Only literal type is supported; encrypted variants are rejected (use literal).
func resolveSystemPrompt(sv v1alpha1.SettingValue) (string, error) {
	switch sv.Type {
	case v1alpha1.SettingLiteral:
		var s string
		if err := json.Unmarshal(sv.Value, &s); err != nil {
			return "", fmt.Errorf("system_prompt: literal value must be a JSON string: %w", err)
		}
		return s, nil
	case "":
		return "", nil
	default:
		return "", fmt.Errorf("system_prompt: type %q is not yet supported (use literal)", sv.Type)
	}
}

// collectCapabilitySettings builds the CapabilitySettings map for a binding.
func collectCapabilitySettings(caps []v1alpha1.Capability) map[string]map[string]v1alpha1.SettingValue {
	out := make(map[string]map[string]v1alpha1.SettingValue, len(caps))
	for _, c := range caps {
		out[c.Name] = c.Settings
	}
	return out
}

func validateSubjects(users, scopes []string) error {
	if len(users) == 0 || len(scopes) == 0 {
		return fmt.Errorf("users and scopes are required")
	}
	return nil
}

// Digest returns the canonical content digest of a manifest, matching the digest
// recorded on a resolved binding. It lets callers group threads by the manifest
// that produced them.
func Digest(m aurora.Manifest) string { return digest(m) }

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
