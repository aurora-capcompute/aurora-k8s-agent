package assembly

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/aurora-capcompute/aurora-capcompute/aurora"
	"github.com/aurora-capcompute/capcompute/dispatcher"

	"github.com/aurora-capcompute/aurora-k8s-agent/internal/apis/aurora/v1alpha1"
)

// nsProvider implements aurora.DispatcherProvider with a Normalize that passes
// settings through, validating nothing (so BuildManifest tests can focus on
// the assembly logic).
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

// rejectProvider returns an error for every Normalize call.
type rejectProvider struct{}

func (rejectProvider) Normalize(name string, _ json.RawMessage) (json.RawMessage, error) {
	return nil, fmt.Errorf("capability %q: not available", name)
}
func (rejectProvider) NewDispatcher(context.Context, aurora.RunContext, aurora.Manifest) (dispatcher.Dispatcher[aurora.RunContext], error) {
	return nil, nil
}
func (rejectProvider) IsSubset(_ string, _, _ json.RawMessage) error { return nil }

func TestBuildManifest(t *testing.T) {
	caps := []v1alpha1.Capability{
		{Name: "k8s.get"},
		{Name: "k8s.apply"},
		{Name: "openai.chat", Settings: map[string]v1alpha1.SettingValue{
			"base_url": {Type: v1alpha1.SettingLiteral, Value: json.RawMessage(`"https://api.openai.com/v1"`)},
		}},
	}

	m, err := BuildManifest("sha256:abc/ops", "You are helpful.", "my-binding", "sha256:abc", caps, nil, nsProvider{})
	if err != nil {
		t.Fatalf("BuildManifest: %v", err)
	}
	if m.Brain != "sha256:abc/ops" {
		t.Fatalf("Brain = %q", m.Brain)
	}
	if m.BindingRef != "my-binding" {
		t.Fatalf("BindingRef = %q", m.BindingRef)
	}
	if m.SystemPrompt != "You are helpful." {
		t.Fatalf("SystemPrompt = %q", m.SystemPrompt)
	}
	if len(m.Capabilities) != 3 {
		t.Fatalf("capabilities = %v", m.Capabilities)
	}
}

func TestBuildManifestCapabilityRejected(t *testing.T) {
	// rejectProvider rejects all capabilities; any rejected capability fails the
	// whole manifest (there is no optional escape hatch).
	caps := []v1alpha1.Capability{
		{Name: "k8s.get"},
		{Name: "openai.chat"},
	}
	_, err := BuildManifest("brain", "", "b", "digest", caps, nil, rejectProvider{})
	if err == nil {
		t.Fatal("a capability rejected by Normalize should fail BuildManifest")
	}
}

func TestBuildManifestChildren(t *testing.T) {
	children := []v1alpha1.ChildSpec{{
		Name:         "researcher",
		Brain:        "ops",
		SystemPrompt: "You are a researcher.",
		Capabilities: []v1alpha1.Capability{{Name: "llm.chat"}},
	}}
	m, err := BuildManifest("sha256:abc/ops", "", "b", "sha256:abc", nil, children, nsProvider{})
	if err != nil {
		t.Fatalf("BuildManifest: %v", err)
	}
	if len(m.Children) != 1 {
		t.Fatalf("children = %v", m.Children)
	}
	ch := m.Children[0]
	if ch.Name != "researcher" {
		t.Fatalf("child name = %q", ch.Name)
	}
	// Brain is expanded to artifactDigest/brainName.
	if ch.Brain != "sha256:abc/ops" {
		t.Fatalf("child Brain = %q, want sha256:abc/ops", ch.Brain)
	}
	if ch.SystemPrompt != "You are a researcher." {
		t.Fatalf("child SystemPrompt = %q", ch.SystemPrompt)
	}
	if ch.BindingRef != "b" {
		t.Fatalf("child BindingRef = %q", ch.BindingRef)
	}
}
