// Package webchannel is the in-process "web" communication channel. It tracks the
// manifests bound to web channels via the control plane so a UI can switch between
// them, and maps a thread back to the manifest that created it (by content digest)
// so all data can be grouped per manifest — without any extra persistence, since a
// thread already carries its manifest.
package webchannel

import (
	"crypto/subtle"
	"log/slog"
	"sort"
	"sync"

	"github.com/aurora-capcompute/aurora-capcompute/aurora"

	"github.com/aurora-capcompute/aurora-k8s-agent/internal/controller"
	"github.com/aurora-capcompute/aurora-k8s-agent/internal/secrets"
)

// ManifestInfo describes a manifest bound to the web channel, for the UI switcher.
type ManifestInfo struct {
	Name         string          `json:"name"`
	Brain        string          `json:"brain"`
	SystemPrompt string          `json:"system_prompt,omitempty"`
	Capabilities []string        `json:"capabilities"`
	Digest       string          `json:"digest"`
	Manifest     aurora.Manifest `json:"manifest"`
}

type entry struct {
	manifest aurora.Manifest
	info     ManifestInfo
	token    []byte // nil = no auth required for this binding
}

type resolvedUser struct {
	name     string
	password []byte
}

// channelAuth holds the resolved bearer token and login credentials for one web
// channel declared in a Manifest. Multiple bindings can target the same channel;
// they all share its token and user list.
type channelAuth struct {
	token []byte
	users []resolvedUser
}

// Channel holds the web channel's bound manifests, rebuilt from each control-plane
// reconciliation.
type Channel struct {
	mu       sync.RWMutex
	byName   map[string]entry       // binding name → manifest + token
	byDigest map[string]string      // manifest digest → binding name
	channels map[string]channelAuth // scoped channel name → token + users
}

// New returns an empty web channel registry.
func New() *Channel {
	return &Channel{
		byName:   map[string]entry{},
		byDigest: map[string]string{},
		channels: map[string]channelAuth{},
	}
}

// Apply rebuilds the registry from a control-plane resolution, keeping only the
// bindings that target the web channel. resolver is used to decrypt channel
// tokens and user passwords; pass nil when running without a control-plane
// secret key (no auth).
func (c *Channel) Apply(res controller.Resolved, resolver secrets.Resolver) {
	channels := make(map[string]channelAuth)
	bindingTokens := make(map[string][]byte)

	if resolver != nil {
		for _, ch := range res.Channels {
			if ch.Source != "web" {
				continue
			}
			var tok []byte
			if src, ok := ch.Secrets["token"]; ok {
				t, err := resolver.Resolve(src)
				if err != nil {
					slog.Warn("webchannel: resolve token", "channel", ch.Name, "error", err)
				} else {
					tok = t
				}
			}
			var users []resolvedUser
			for _, u := range ch.Users {
				pw, err := resolver.Resolve(u.Password)
				if err != nil {
					slog.Warn("webchannel: resolve user password",
						"channel", ch.Name, "user", u.Name, "error", err)
					continue
				}
				users = append(users, resolvedUser{name: u.Name, password: pw})
			}
			channels[ch.Name] = channelAuth{token: tok, users: users}
			for _, b := range ch.Bindings {
				bindingTokens[b.BindingRef] = tok
			}
		}
	}

	byName := make(map[string]entry)
	byDigest := make(map[string]string)
	for _, b := range res.Bindings {
		if b.Source != "web" {
			continue
		}
		capabilities := make([]string, 0, len(b.Manifest.Tools))
		for _, t := range b.Manifest.Tools {
			capabilities = append(capabilities, t.Name)
		}
		info := ManifestInfo{
			Name:         b.Name,
			Brain:        b.Manifest.Brain,
			SystemPrompt: b.Manifest.SystemPrompt,
			Capabilities: capabilities,
			Digest:       b.Digest,
			Manifest:     b.Manifest,
		}
		byName[b.Name] = entry{manifest: b.Manifest, info: info, token: bindingTokens[b.Name]}
		byDigest[b.Digest] = b.Name
	}

	c.mu.Lock()
	c.byName, c.byDigest, c.channels = byName, byDigest, channels
	c.mu.Unlock()
}

// Manifests lists the bound manifests, sorted by name.
func (c *Channel) Manifests() []ManifestInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]ManifestInfo, 0, len(c.byName))
	for _, e := range c.byName {
		out = append(out, e.info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Manifest returns the manifest bound under name.
func (c *Channel) Manifest(name string) (aurora.Manifest, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.byName[name]
	return e.manifest, ok
}

// Has reports whether a manifest is bound under name.
func (c *Channel) Has(name string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	_, ok := c.byName[name]
	return ok
}

// HasAccess reports whether bearerToken grants access to the named binding.
// Returns true when the binding exists in the web channel AND either no token
// is configured for it, or bearerToken matches (constant-time compare).
// Returns false when the binding is not found (not a web binding) or the token
// is wrong.
func (c *Channel) HasAccess(name, bearerToken string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.byName[name]
	if !ok {
		return false
	}
	if e.token == nil {
		return true
	}
	return subtle.ConstantTimeCompare([]byte(bearerToken), e.token) == 1
}

// Login validates name/password against the user list of all web channels and,
// on success, returns the channel's bearer token. The returned token is the
// plaintext string that the client should send as Authorization: Bearer <token>.
// Returns nil, false if no user matches.
func (c *Channel) Login(name, password string) ([]byte, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, auth := range c.channels {
		for _, u := range auth.users {
			if u.name != name {
				continue
			}
			if subtle.ConstantTimeCompare([]byte(password), u.password) == 1 {
				return auth.token, true
			}
			return nil, false // username matched but wrong password — don't check other channels
		}
	}
	return nil, false
}

// NameForManifest returns the bound name whose manifest matches m by content
// digest — i.e. the manifest a thread belongs to.
func (c *Channel) NameForManifest(m aurora.Manifest) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	name, ok := c.byDigest[controller.Digest(m)]
	return name, ok
}
