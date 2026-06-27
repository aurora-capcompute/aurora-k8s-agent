package assembly

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/aurora-capcompute/aurora-capcompute/aurora"
	"github.com/aurora-capcompute/capcompute/dispatcher"

	"github.com/aurora-capcompute/aurora-k8s-agent/internal/brainspec"
	"github.com/aurora-capcompute/aurora-k8s-agent/internal/oci"
)

// nsProvider implements aurora.DispatcherProvider with a namespace-subset rule,
// mirroring the real k8s IsSubset: an empty parent namespace list allows anything.
type nsProvider struct{}

func (nsProvider) Normalize(_ string, s json.RawMessage) (json.RawMessage, error) {
	if len(s) == 0 {
		return json.RawMessage(`{}`), nil
	}
	return s, nil
}

func (nsProvider) NewDispatcher(context.Context, aurora.RunContext, aurora.Manifest) (dispatcher.Dispatcher[aurora.RunContext], error) {
	return nil, nil
}

func (nsProvider) IsSubset(_ string, parent, child json.RawMessage) error {
	type settings struct {
		Namespaces []string `json:"namespaces"`
	}
	var p, c settings
	_ = json.Unmarshal(parent, &p)
	_ = json.Unmarshal(child, &c)
	if len(p.Namespaces) == 0 {
		return nil
	}
	allowed := make(map[string]struct{}, len(p.Namespaces))
	for _, ns := range p.Namespaces {
		allowed[ns] = struct{}{}
	}
	for _, ns := range c.Namespaces {
		if _, ok := allowed[ns]; !ok {
			return fmt.Errorf("namespace %q not allowed", ns)
		}
	}
	return nil
}

func caps(names ...string) []aurora.CapabilityConfig {
	out := make([]aurora.CapabilityConfig, len(names))
	for i, n := range names {
		out[i] = aurora.CapabilityConfig{Name: n}
	}
	return out
}

func TestValidateGrant(t *testing.T) {
	decl := brainspec.Manifest{ID: "ops", Capabilities: []brainspec.Capability{
		{Name: "k8s.get"},
		{Name: "k8s.apply", Optional: true},
		{Name: "k8s.list", Settings: json.RawMessage(`{"namespaces":["default","kube-system"]}`)},
	}}

	// Required present, optional omitted, scoped subset allowed.
	granted := []aurora.CapabilityConfig{
		{Name: "k8s.get"},
		{Name: "k8s.list", Settings: json.RawMessage(`{"namespaces":["default"]}`)},
	}
	if err := ValidateGrant(decl, granted, nsProvider{}); err != nil {
		t.Fatalf("valid grant rejected: %v", err)
	}

	// Undeclared capability.
	if err := ValidateGrant(decl, caps("k8s.get", "helm.upgrade"), nsProvider{}); err == nil {
		t.Fatal("undeclared capability should be rejected")
	}
	// Missing required capability (only the optional one granted).
	if err := ValidateGrant(decl, caps("k8s.apply", "k8s.list"), nsProvider{}); err == nil {
		t.Fatal("missing required capability should be rejected")
	}
	// Settings exceed the declared namespace scope.
	wide := []aurora.CapabilityConfig{
		{Name: "k8s.get"},
		{Name: "k8s.list", Settings: json.RawMessage(`{"namespaces":["secret-ns"]}`)},
	}
	if err := ValidateGrant(decl, wide, nsProvider{}); err == nil {
		t.Fatal("settings exceeding declaration should be rejected")
	}
}

func TestValidateChildrenSubset(t *testing.T) {
	manifest := aurora.Manifest{
		Version:      aurora.ManifestVersion,
		Capabilities: []aurora.CapabilityConfig{{Name: "k8s.get", Settings: json.RawMessage(`{"namespaces":["default"]}`)}},
		Children: []aurora.ChildManifest{{
			Name:         "reader",
			Brain:        "ops",
			Capabilities: []aurora.CapabilityConfig{{Name: "k8s.get", Settings: json.RawMessage(`{"namespaces":["default"]}`)}},
		}},
	}
	if err := ValidateChildrenSubset(manifest, nsProvider{}); err != nil {
		t.Fatalf("valid child rejected: %v", err)
	}

	manifest.Children[0].Capabilities[0].Settings = json.RawMessage(`{"namespaces":["other"]}`)
	if err := ValidateChildrenSubset(manifest, nsProvider{}); err == nil {
		t.Fatal("child exceeding parent namespace should be rejected")
	}

	manifest.Children[0].Capabilities = []aurora.CapabilityConfig{{Name: "helm.upgrade"}}
	if err := ValidateChildrenSubset(manifest, nsProvider{}); err == nil {
		t.Fatal("child using a capability the parent lacks should be rejected")
	}
}

func TestProviderValidateManifest(t *testing.T) {
	decl := brainspec.Manifest{ID: "ops", Capabilities: []brainspec.Capability{{Name: "k8s.get"}}}
	puller := fakePuller{byRef: map[string]oci.Artifact{
		"ref": {Manifest: decl, Wasm: []byte("\x00asm"), Digest: "sha256:ops"},
	}}
	p, err := NewOCIBrainProvider(context.Background(), []string{"ref"}, "ops", puller)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	ok := aurora.Manifest{Version: aurora.ManifestVersion, Brain: "ops", Capabilities: caps("k8s.get")}
	if err := p.ValidateManifest(ok, nsProvider{}); err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}
	bad := aurora.Manifest{Version: aurora.ManifestVersion, Brain: "ops", Capabilities: caps("k8s.delete")}
	if err := p.ValidateManifest(bad, nsProvider{}); err == nil {
		t.Fatal("undeclared capability should be rejected")
	}
	unknown := aurora.Manifest{Version: aurora.ManifestVersion, Brain: "ghost", Capabilities: caps("k8s.get")}
	if err := p.ValidateManifest(unknown, nsProvider{}); err == nil {
		t.Fatal("unknown brain should be rejected")
	}
}
