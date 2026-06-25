// Package webchannel is the in-process "web" communication channel. It tracks the
// manifests (FunctionInstances) bound to web Channels via the control plane so a
// UI can switch between them, and maps a thread back to the manifest that created
// it (by content digest) so all data can be grouped per manifest — without any
// extra persistence, since a thread already carries its manifest.
package webchannel

import (
	"sort"
	"sync"

	"aurora-capcompute/aurora"

	"aurora-k8s-agent/internal/controller"
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
}

// Channel holds the web channel's bound manifests, rebuilt from each control-plane
// reconciliation.
type Channel struct {
	mu       sync.RWMutex
	byName   map[string]entry
	byDigest map[string]string // manifest digest -> bound name
}

// New returns an empty web channel registry.
func New() *Channel {
	return &Channel{byName: map[string]entry{}, byDigest: map[string]string{}}
}

// Apply rebuilds the registry from a control-plane resolution, keeping only the
// bindings that target the web channel.
func (c *Channel) Apply(res controller.Resolved) {
	byName := make(map[string]entry)
	byDigest := make(map[string]string)
	for _, b := range res.Bindings {
		if b.Source != "web" {
			continue
		}
		capabilities := make([]string, len(b.Manifest.Capabilities))
		for i, capability := range b.Manifest.Capabilities {
			capabilities[i] = capability.Name
		}
		info := ManifestInfo{
			Name:         b.Name,
			Brain:        b.Manifest.Brain,
			SystemPrompt: b.Manifest.SystemPrompt,
			Capabilities: capabilities,
			Digest:       b.Digest,
			Manifest:     b.Manifest,
		}
		byName[b.Name] = entry{manifest: b.Manifest, info: info}
		byDigest[b.Digest] = b.Name
	}
	c.mu.Lock()
	c.byName, c.byDigest = byName, byDigest
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

// NameForManifest returns the bound name whose manifest matches m by content
// digest — i.e. the manifest a thread belongs to.
func (c *Channel) NameForManifest(m aurora.Manifest) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	name, ok := c.byDigest[controller.Digest(m)]
	return name, ok
}
