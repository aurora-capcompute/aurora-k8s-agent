// Package controller resolves the Aurora control-plane resource — the single
// Manifest CRD — into the agent's runtime configuration: which brain artifacts
// to load, and which validated bindings to serve. Reconcile is a pure function
// over decoded specs so it can be unit-tested without a cluster; the informer
// wiring that feeds it lives alongside.
//
// The model: a Manifest inlines a brain OCI artifact (which bundles named WASM
// binaries), an embedded array of typed-ADT channels (each transport plus its
// native subjects and credentials), and the full capability tree (capabilities
// + children). One Manifest is validated and projected onto one binding per
// channel it serves.
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

// NamedManifest pairs a Manifest's name with its decoded spec.
type NamedManifest struct {
	Name string
	Spec v1alpha1.ManifestSpec
}

// Inputs is the full set of control-plane resources to resolve.
type Inputs struct {
	Manifests []NamedManifest
}

// SourceBinding is a validated Manifest projected onto one transport source.
type SourceBinding struct {
	Source string
	// Name is the Manifest name, so a binding is addressable (e.g. by a web UI
	// switching between the manifests serving a channel).
	Name string
	binding.Resolved
}

// Resolved is the outcome of reconciliation: the brain artifacts to load, the
// bindings to serve, the live channels, and the status to write back to each
// Manifest (keyed by Manifest name).
type Resolved struct {
	BrainRefs      []string
	Brains         []ResolvedBrain
	Bindings       []SourceBinding
	Channels       []ResolvedChannel
	ManifestStatus map[string]v1alpha1.ManifestStatus
}

// ResolvedChannel groups a ready typed channel with the bindings that target it,
// so a channel supervisor can construct one live bridge per channel declared in a
// Manifest. Name is the Manifest-scoped channel identity (manifestName/channelName).
// Secrets carries the channel's unresolved credential sources (keyed e.g.
// "botToken", "appToken"); the supervisor holds the key and resolves them. Users
// carries web-channel login credentials (unresolved); webchannel.Channel.Apply
// resolves them with the same key.
type ResolvedChannel struct {
	Kind     string
	Name     string
	Source   string
	Secrets  map[string]v1alpha1.SecretSource
	Users    []v1alpha1.WebChannelUser // web channels only
	Bindings []binding.Resolved
}

// ChannelKey is the registry/running-set key for a resolved channel, namespaced
// by kind. name is the Manifest-scoped channel identity (manifestName/channelName).
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

// Reconcile resolves inputs into runtime config and per-Manifest status. It never
// errors: every failure is recorded as a not-ready status on the offending
// Manifest, so a single bad object cannot block the rest.
func Reconcile(ctx context.Context, in Inputs, puller oci.Puller, provider aurora.DispatcherProvider) Resolved {
	res := Resolved{
		ManifestStatus: make(map[string]v1alpha1.ManifestStatus, len(in.Manifests)),
	}

	// Brain artifacts are pulled once per ref and shared across Manifests.
	brains := make(map[string]loadedBrain)
	channels := make(map[string]*resolvedChannel)
	refs := make(map[string]struct{})

	for _, m := range in.Manifests {
		ref := m.Spec.Brain.Artifact
		brain, ok := brains[ref]
		if !ok {
			artifact, err := puller.Pull(ctx, ref)
			if err != nil {
				res.ManifestStatus[m.Name] = manifestNotReady("pull %s: %v", ref, err)
				continue
			}
			brain = loadedBrain{brains: artifact.Brains, main: artifact.Main, digest: artifact.Digest, ref: ref}
			brains[ref] = brain
		}

		// Validate the inline channels before doing any other work, so a bad
		// channel fails the whole Manifest with a clear message.
		if len(m.Spec.Channels) == 0 {
			res.ManifestStatus[m.Name] = manifestNotReady("no channels configured")
			continue
		}
		resolvedChans, err := resolveManifestChannels(m.Name, m.Spec.Channels)
		if err != nil {
			res.ManifestStatus[m.Name] = manifestNotReady("%v", err)
			continue
		}

		if err := validateChildBrains(m.Spec.Tools, brain.brains); err != nil {
			res.ManifestStatus[m.Name] = manifestNotReady("%v", err)
			continue
		}

		if err := validateAllToolSettings(m.Spec.Tools); err != nil {
			res.ManifestStatus[m.Name] = manifestNotReady("%v", err)
			continue
		}

		systemPrompt, err := resolveSystemPrompt(m.Spec.SystemPrompt)
		if err != nil {
			res.ManifestStatus[m.Name] = manifestNotReady("%v", err)
			continue
		}

		rootBrainID := brain.digest + "/" + brain.main
		manifest, err := assembly.BuildManifest(rootBrainID, systemPrompt, m.Name, brain.digest,
			m.Spec.Tools, provider)
		if err != nil {
			res.ManifestStatus[m.Name] = manifestNotReady("%v", err)
			continue
		}

		capSettings := collectToolSettings(m.Spec.Tools)
		md := digest(manifest)

		for _, ch := range resolvedChans {
			channels[ChannelKey(ch.kind, ch.name)] = ch
			resolved := binding.Resolved{
				Users:              append([]string(nil), ch.users...),
				Scopes:             append([]string(nil), ch.scopes...),
				Manifest:           manifest,
				Digest:             md,
				CapabilitySettings: capSettings,
				BindingRef:         m.Name,
			}
			res.Bindings = append(res.Bindings, SourceBinding{Source: ch.source, Name: m.Name, Resolved: resolved})
			ch.bindings = append(ch.bindings, resolved)
		}
		refs[brain.ref] = struct{}{}
		res.ManifestStatus[m.Name] = v1alpha1.ManifestStatus{Ready: true, Digest: md, BrainID: rootBrainID}
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

// resolveManifestChannels validates a Manifest's inline channels and returns them
// normalised to transport sources. Channel identity is scoped by the Manifest
// name (manifestName/channelName) so channels with the same name in different
// Manifests do not collide on the supervisor's running set or sqlite filenames.
// A duplicate or invalid channel fails the whole Manifest.
func resolveManifestChannels(manifestName string, chans []v1alpha1.Channel) ([]*resolvedChannel, error) {
	out := make([]*resolvedChannel, 0, len(chans))
	seen := make(map[string]bool, len(chans))
	for i, c := range chans {
		if c.Name == "" {
			return nil, fmt.Errorf("channel %d: name is required", i)
		}
		// Names are scoped by kind, so the same name may be reused across kinds
		// (e.g. a TelegramChannel and a WebChannel both named "ops").
		key := c.Kind + "/" + c.Name
		if seen[key] {
			return nil, fmt.Errorf("channel %q: duplicate %s name", c.Name, c.Kind)
		}
		seen[key] = true
		rc, err := resolveChannel(manifestName+"/"+c.Name, c)
		if err != nil {
			return nil, err
		}
		out = append(out, rc)
	}
	return out, nil
}

// resolveChannel validates one typed-ADT channel and normalises it to a transport
// source plus its subjects and credential sources. Slack/Telegram require
// credentials and non-empty subjects; Web carries an optional token and may omit
// subjects. name is the Manifest-scoped channel identity.
func resolveChannel(name string, c v1alpha1.Channel) (*resolvedChannel, error) {
	switch c.Kind {
	case v1alpha1.KindSlackChannel:
		s := c.Slack
		if s == nil {
			return nil, fmt.Errorf("channel %q: slack payload is required for kind SlackChannel", c.Name)
		}
		if err := validateSecret("appToken", s.AppToken); err != nil {
			return nil, fmt.Errorf("channel %q: %w", c.Name, err)
		}
		if err := validateSecret("botToken", s.BotToken); err != nil {
			return nil, fmt.Errorf("channel %q: %w", c.Name, err)
		}
		if err := validateSubjects(s.Users, s.Scopes); err != nil {
			return nil, fmt.Errorf("channel %q: %w", c.Name, err)
		}
		return &resolvedChannel{
			kind: v1alpha1.KindSlackChannel, name: name, source: "slack",
			users: s.Users, scopes: s.Scopes,
			secrets: map[string]v1alpha1.SecretSource{"appToken": s.AppToken, "botToken": s.BotToken},
		}, nil
	case v1alpha1.KindTelegramChannel:
		t := c.Telegram
		if t == nil {
			return nil, fmt.Errorf("channel %q: telegram payload is required for kind TelegramChannel", c.Name)
		}
		if err := validateSecret("botToken", t.BotToken); err != nil {
			return nil, fmt.Errorf("channel %q: %w", c.Name, err)
		}
		if err := validateSubjects(t.Users, t.Scopes); err != nil {
			return nil, fmt.Errorf("channel %q: %w", c.Name, err)
		}
		return &resolvedChannel{
			kind: v1alpha1.KindTelegramChannel, name: name, source: "telegram",
			users: t.Users, scopes: t.Scopes,
			secrets: map[string]v1alpha1.SecretSource{"botToken": t.BotToken},
		}, nil
	case v1alpha1.KindWebChannel:
		w := c.Web
		if w == nil {
			w = &v1alpha1.WebChannelSpec{}
		}
		var chSecrets map[string]v1alpha1.SecretSource
		if w.Token != nil {
			if err := validateSecret("token", *w.Token); err != nil {
				return nil, fmt.Errorf("channel %q: invalid token: %w", c.Name, err)
			}
			chSecrets = map[string]v1alpha1.SecretSource{"token": *w.Token}
		}
		for i, u := range w.Users {
			if err := validateSecret(fmt.Sprintf("users[%d].password", i), u.Password); err != nil {
				return nil, fmt.Errorf("channel %q: %w", c.Name, err)
			}
		}
		return &resolvedChannel{
			kind: v1alpha1.KindWebChannel, name: name, source: "web",
			webUsers: w.Users, scopes: w.Scopes,
			secrets: chSecrets,
		}, nil
	case "":
		return nil, fmt.Errorf("channel %q: kind is required", c.Name)
	default:
		return nil, fmt.Errorf("channel %q: unknown kind %q", c.Name, c.Kind)
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
func validateToolSettings(toolName string, settings map[string]v1alpha1.SettingValue) error {
	for k, sv := range settings {
		switch sv.Type {
		case v1alpha1.SettingLiteral:
			if len(sv.Value) == 0 {
				return fmt.Errorf("tool %q setting %q: literal value is required", toolName, k)
			}
		case v1alpha1.SecretInPlaceEncrypted:
			if sv.Ciphertext == "" {
				return fmt.Errorf("tool %q setting %q: ciphertext is required for inPlaceEncrypted", toolName, k)
			}
		case v1alpha1.SecretStorage:
			if sv.Ref == nil || sv.Ref.Name == "" || sv.Ref.Key == "" {
				return fmt.Errorf("tool %q setting %q: ref.{name,key} are required for secretStorage", toolName, k)
			}
		case "":
			return fmt.Errorf("tool %q setting %q: type is required", toolName, k)
		default:
			return fmt.Errorf("tool %q setting %q: unknown type %q", toolName, k, sv.Type)
		}
	}
	return nil
}

// validateAllToolSettings recursively validates ADT structure for every tool in
// the composition tree.
func validateAllToolSettings(tools []v1alpha1.Tool) error {
	for _, t := range tools {
		if err := validateToolSettings(t.Name, t.Settings); err != nil {
			return err
		}
		if t.Type == v1alpha1.AgentToolType {
			if err := validateAllToolSettings(t.Tools); err != nil {
				return err
			}
		}
	}
	return nil
}

// validateChildBrains checks that every core.agent tool's settings.code names a
// WASM bundled in the artifact. Typos or missing bundles surface at reconcile
// time rather than as confusing runtime delegation failures.
func validateChildBrains(tools []v1alpha1.Tool, available map[string][]byte) error {
	for _, t := range tools {
		if t.Type != v1alpha1.AgentToolType {
			continue
		}
		code := agentCode(t)
		if _, ok := available[code]; !ok {
			return fmt.Errorf("agent %q references brain %q which is not bundled in the artifact", t.Name, code)
		}
		if err := validateChildBrains(t.Tools, available); err != nil {
			return err
		}
	}
	return nil
}

// agentCode extracts a core.agent tool's `code` (short WASM name) literal.
func agentCode(t v1alpha1.Tool) string {
	sv, ok := t.Settings["code"]
	if !ok || sv.Type != v1alpha1.SettingLiteral || len(sv.Value) == 0 {
		return ""
	}
	var s string
	_ = json.Unmarshal(sv.Value, &s)
	return s
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

// collectToolSettings builds the warmup CapabilitySettings map for a binding
// from the root's leaf tools (children fall back to normalized manifest settings
// at dispatch time), keyed by tool name.
func collectToolSettings(tools []v1alpha1.Tool) map[string]map[string]v1alpha1.SettingValue {
	out := make(map[string]map[string]v1alpha1.SettingValue, len(tools))
	for _, t := range tools {
		if t.Type == v1alpha1.AgentToolType {
			continue
		}
		out[t.Name] = t.Settings
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

func manifestNotReady(format string, args ...any) v1alpha1.ManifestStatus {
	return v1alpha1.ManifestStatus{Message: fmt.Sprintf(format, args...)}
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
