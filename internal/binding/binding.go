// Package binding resolves the named-manifest + bindings policy format shared by
// every source: a manifest is defined once, by name, and bound to a set of
// (source, subject, scope) tuples. This replaces copying a manifest into each
// user entry. The legacy per-channel `users` format is still accepted by the
// policy loaders; IsBindingFormat distinguishes the two. See
// docs/rfc-sources-and-bindings.md.
package binding

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aurora-capcompute/aurora-capcompute/aurora"
)

// Version is the schema version of the bindings format.
const Version = 2

// File is the on-disk bindings document.
type File struct {
	Version   int                        `json:"version"`
	Manifests map[string]aurora.Manifest `json:"manifests"`
	Bindings  []Binding                  `json:"bindings"`
}

// Binding grants a named manifest to a set of subjects within a set of scopes on
// one source. For Telegram, users and scopes are numeric IDs (as strings); for
// Slack, user IDs (U…) and channel IDs (C…/G…/D…).
type Binding struct {
	Source   string   `json:"source"`
	Manifest string   `json:"manifest"`
	Users    []string `json:"users"`
	Scopes   []string `json:"scopes"`
}

// Resolved is one validated binding for a single source: the subjects and scopes
// it grants, plus the resolved manifest and its digest.
type Resolved struct {
	Users    []string
	Scopes   []string
	Manifest aurora.Manifest
	Digest   string
}

// IsBindingFormat reports whether raw uses the named-manifest bindings format
// rather than the legacy per-channel `users` format.
func IsBindingFormat(raw []byte) bool {
	var probe struct {
		Manifests json.RawMessage `json:"manifests"`
		Bindings  json.RawMessage `json:"bindings"`
	}
	if json.Unmarshal(raw, &probe) != nil {
		return false
	}
	return len(probe.Manifests) > 0 || len(probe.Bindings) > 0
}

// ForSource validates raw and returns the bindings that apply to sourceKind.
// Manifests are validated with provider and digested exactly as the legacy
// loaders do, so migrating a manifest verbatim preserves its digest (and avoids
// forcing users to re-confirm).
func ForSource(raw []byte, sourceKind string, provider aurora.DispatcherProvider) ([]Resolved, error) {
	var file File
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&file); err != nil {
		return nil, fmt.Errorf("decode bindings: %w", err)
	}
	if file.Version != Version {
		return nil, fmt.Errorf("unsupported bindings version %d (want %d)", file.Version, Version)
	}
	if len(file.Manifests) == 0 {
		return nil, fmt.Errorf("bindings must define at least one manifest")
	}
	sourceKind = strings.ToLower(strings.TrimSpace(sourceKind))

	type entry struct {
		manifest aurora.Manifest
		digest   string
	}
	cache := make(map[string]entry)
	resolve := func(name string) (entry, error) {
		if e, ok := cache[name]; ok {
			return e, nil
		}
		m, ok := file.Manifests[name]
		if !ok {
			return entry{}, fmt.Errorf("unknown manifest %q", name)
		}
		validated, err := aurora.ValidateManifest(m, provider)
		if err != nil {
			return entry{}, fmt.Errorf("manifest %q: %w", name, err)
		}
		input, _ := json.Marshal(validated)
		sum := sha256.Sum256(input)
		e := entry{manifest: validated, digest: hex.EncodeToString(sum[:])}
		cache[name] = e
		return e, nil
	}

	var out []Resolved
	for i, b := range file.Bindings {
		if strings.ToLower(strings.TrimSpace(b.Source)) != sourceKind {
			continue
		}
		name := strings.TrimSpace(b.Manifest)
		if name == "" {
			return nil, fmt.Errorf("binding %d: missing manifest", i)
		}
		e, err := resolve(name)
		if err != nil {
			return nil, fmt.Errorf("binding %d: %w", i, err)
		}
		users := trimNonEmpty(b.Users)
		if len(users) == 0 {
			return nil, fmt.Errorf("binding %d (%s): must list at least one user", i, sourceKind)
		}
		scopes := trimNonEmpty(b.Scopes)
		if len(scopes) == 0 {
			return nil, fmt.Errorf("binding %d (%s): must list at least one scope", i, sourceKind)
		}
		out = append(out, Resolved{Users: users, Scopes: scopes, Manifest: e.manifest, Digest: e.digest})
	}
	return out, nil
}

func trimNonEmpty(in []string) []string {
	var out []string
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}
