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
// settings through, validating nothing (so BuildManifest tests can focus on the
// assembly logic).
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

// rejectProvider returns an error for every Normalize call.
type rejectProvider struct{}

func (rejectProvider) Normalize(toolType string, _ json.RawMessage) (json.RawMessage, error) {
	return nil, fmt.Errorf("tool type %q: not available", toolType)
}
func (rejectProvider) NewDispatcher(context.Context, aurora.RunContext, aurora.Manifest) (dispatcher.Dispatcher[aurora.RunContext], error) {
	return nil, nil
}

func TestBuildManifest(t *testing.T) {
	tools := []v1alpha1.Tool{
		{Name: "cluster", Type: "core.k8s"},
		{Name: "llm", Type: "core.openaiApi", Hidden: true, Settings: map[string]v1alpha1.SettingValue{
			"base_url": {Type: v1alpha1.SettingLiteral, Value: json.RawMessage(`"https://api.openai.com/v1"`)},
		}},
	}

	m, err := BuildManifest("sha256:abc/ops", "You are helpful.", "my-binding", "sha256:abc", tools, nsProvider{})
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
	if len(m.Tools) != 2 {
		t.Fatalf("tools = %v", m.Tools)
	}
	if !m.Tools[1].Hidden {
		t.Fatalf("llm tool should be hidden: %+v", m.Tools[1])
	}
}

func TestBuildManifestToolRejected(t *testing.T) {
	// rejectProvider rejects every leaf tool; any rejection fails the whole
	// manifest (there is no optional escape hatch).
	tools := []v1alpha1.Tool{{Name: "cluster", Type: "core.k8s"}}
	if _, err := BuildManifest("brain", "", "b", "digest", tools, rejectProvider{}); err == nil {
		t.Fatal("a tool rejected by Normalize should fail BuildManifest")
	}
}

func TestBuildManifestAgentTool(t *testing.T) {
	tools := []v1alpha1.Tool{{
		Name: "researcher",
		Type: v1alpha1.AgentToolType,
		Settings: map[string]v1alpha1.SettingValue{
			"code":          {Type: v1alpha1.SettingLiteral, Value: json.RawMessage(`"ops"`)},
			"system_prompt": {Type: v1alpha1.SettingLiteral, Value: json.RawMessage(`"You are a researcher."`)},
		},
		Tools: []v1alpha1.Tool{{Name: "llm", Type: "core.openaiApi"}},
	}}
	m, err := BuildManifest("sha256:abc/ops", "", "b", "sha256:abc", tools, nsProvider{})
	if err != nil {
		t.Fatalf("BuildManifest: %v", err)
	}
	if len(m.Tools) != 1 {
		t.Fatalf("tools = %v", m.Tools)
	}
	tool := m.Tools[0]
	if tool.Name != "researcher" || tool.Type != v1alpha1.AgentToolType {
		t.Fatalf("agent tool = %+v", tool)
	}
	if len(tool.Tools) != 1 || tool.Tools[0].Name != "llm" {
		t.Fatalf("nested tools = %+v", tool.Tools)
	}
	var as aurora.AgentSettings
	if err := json.Unmarshal(tool.Settings, &as); err != nil {
		t.Fatalf("decode agent settings: %v", err)
	}
	// Code is expanded to artifactDigest/code.
	if as.Code != "sha256:abc/ops" {
		t.Fatalf("agent Code = %q, want sha256:abc/ops", as.Code)
	}
	if as.SystemPrompt != "You are a researcher." {
		t.Fatalf("agent SystemPrompt = %q", as.SystemPrompt)
	}
	if as.BindingRef != "b" {
		t.Fatalf("agent BindingRef = %q", as.BindingRef)
	}
}
