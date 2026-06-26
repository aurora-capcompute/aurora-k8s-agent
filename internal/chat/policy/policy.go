// Package policy is the transport-agnostic authorization set shared by every chat
// adapter. A Set maps a subject (the chat user) to the scopes (chats/channels) it
// may drive the agent in, plus the manifest it runs under. Platforms differ only
// in how a subject/scope string is parsed into its native ID type, so the logic is
// generic over that ID and parameterized by a Config the adapter supplies.
package policy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"aurora-capcompute/aurora"

	"aurora-k8s-agent/internal/binding"
)

// Config carries the platform-specific bits the generic parser needs.
type Config[ID comparable] struct {
	// Source is the binding-format source name (e.g. "telegram", "slack").
	Source string
	// Noun names the platform in error messages (e.g. "Telegram", "Slack").
	Noun string
	// ParseSubject converts a user-ID string into the native ID, rejecting invalid
	// values (e.g. non-positive Telegram user IDs).
	ParseSubject func(string) (ID, error)
	// ParseScope converts a chat/channel-ID string into the native ID. Telegram
	// chats may be negative (supergroups), so subject and scope parsing differ.
	ParseScope func(string) (ID, error)
}

// User is one authorized subject and the scopes it may act in.
type User[ID comparable] struct {
	ID       ID
	scopes   map[ID]struct{}
	Manifest aurora.Manifest
	Digest   string
}

// Set is an immutable authorization set keyed by subject.
type Set[ID comparable] struct {
	users map[ID]User[ID]
}

// Authorize reports whether the subject may drive the agent in the scope.
func (s *Set[ID]) Authorize(subject, scope ID) (User[ID], bool) {
	user, ok := s.users[subject]
	if !ok {
		return User[ID]{}, false
	}
	_, ok = user.scopes[scope]
	return user, ok
}

// Load reads and parses a policy file.
func Load[ID comparable](path string, cfg Config[ID], provider aurora.DispatcherProvider) (*Set[ID], error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read policy: %w", err)
	}
	return Parse(raw, cfg, provider)
}

// Parse builds a Set from either the shared named-manifest bindings format or the
// legacy per-platform policy file (version 1).
func Parse[ID comparable](raw []byte, cfg Config[ID], provider aurora.DispatcherProvider) (*Set[ID], error) {
	if binding.IsBindingFormat(raw) {
		resolved, err := binding.ForSource(raw, cfg.Source, provider)
		if err != nil {
			return nil, err
		}
		return FromResolved(cfg, resolved)
	}
	return parseLegacy(raw, cfg, provider)
}

// scopeToken decodes a scope from either a JSON number (Telegram chat IDs) or a
// JSON string (Slack channel IDs) into its string form, so one struct handles both
// legacy file shapes.
type scopeToken string

func (t *scopeToken) UnmarshalJSON(b []byte) error {
	*t = scopeToken(bytes.Trim(b, `"`))
	return nil
}

type legacyFile struct {
	Version int                   `json:"version"`
	Users   map[string]legacyUser `json:"users"`
}

type legacyUser struct {
	// Both platform field names are accepted; whichever is present supplies the
	// scopes. Unknown fields are still rejected (see DisallowUnknownFields).
	AllowedChats    []scopeToken    `json:"allowed_chats"`
	AllowedChannels []scopeToken    `json:"allowed_channels"`
	Manifest        aurora.Manifest `json:"manifest"`
}

func (u legacyUser) scopeTokens() []scopeToken {
	if u.AllowedChats != nil {
		return u.AllowedChats
	}
	return u.AllowedChannels
}

func parseLegacy[ID comparable](raw []byte, cfg Config[ID], provider aurora.DispatcherProvider) (*Set[ID], error) {
	var file legacyFile
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&file); err != nil {
		return nil, fmt.Errorf("decode policy: %w", err)
	}
	if file.Version != 1 {
		return nil, fmt.Errorf("unsupported policy version %d", file.Version)
	}
	if len(file.Users) == 0 {
		return nil, errors.New("policy must contain at least one user")
	}
	set := &Set[ID]{users: make(map[ID]User[ID], len(file.Users))}
	for rawID, configured := range file.Users {
		subject, err := cfg.ParseSubject(rawID)
		if err != nil {
			return nil, err
		}
		manifest, err := aurora.ValidateManifest(configured.Manifest, provider)
		if err != nil {
			return nil, fmt.Errorf("user %v manifest: %w", subject, err)
		}
		scopes := make(map[ID]struct{})
		for _, token := range configured.scopeTokens() {
			scope, err := cfg.ParseScope(string(token))
			if err != nil {
				return nil, err
			}
			scopes[scope] = struct{}{}
		}
		if len(scopes) == 0 {
			return nil, fmt.Errorf("user %v must allow at least one scope", subject)
		}
		set.users[subject] = User[ID]{
			ID: subject, scopes: scopes, Manifest: manifest, Digest: digest(manifest),
		}
	}
	return set, nil
}

// FromResolved builds a Set from already-resolved bindings (e.g. produced by the
// control plane), so the same routing applies whether bindings come from a file or
// from live channel CRDs.
func FromResolved[ID comparable](cfg Config[ID], resolved []binding.Resolved) (*Set[ID], error) {
	set := &Set[ID]{users: make(map[ID]User[ID])}
	for _, r := range resolved {
		scopes := make(map[ID]struct{}, len(r.Scopes))
		for _, raw := range r.Scopes {
			scope, err := cfg.ParseScope(raw)
			if err != nil {
				return nil, err
			}
			scopes[scope] = struct{}{}
		}
		for _, raw := range r.Users {
			subject, err := cfg.ParseSubject(raw)
			if err != nil {
				return nil, err
			}
			if _, dup := set.users[subject]; dup {
				return nil, fmt.Errorf("%s user %v is bound more than once", cfg.Noun, subject)
			}
			set.users[subject] = User[ID]{ID: subject, scopes: scopes, Manifest: r.Manifest, Digest: r.Digest}
		}
	}
	if len(set.users) == 0 {
		return nil, fmt.Errorf("policy must bind at least one %s user", cfg.Noun)
	}
	return set, nil
}

func digest(manifest aurora.Manifest) string {
	input, _ := json.Marshal(manifest)
	sum := sha256.Sum256(input)
	return hex.EncodeToString(sum[:])
}
