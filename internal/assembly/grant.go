package assembly

import (
	"encoding/json"
	"fmt"

	"github.com/aurora-capcompute/aurora-capcompute/aurora"

	"github.com/aurora-capcompute/aurora-k8s-agent/internal/apis/aurora/v1alpha1"
)

// BuildManifest constructs an aurora.Manifest from the Manifest CRD's unified
// tool tree. Leaf tools are validated via provider.Normalize (keyed by tool
// type); `core.agent` tools carry the sub-agent's brain (settings.code, qualified
// by artifactDigest), system prompt, and failure mode, and recurse into their
// own tools.
func BuildManifest(
	brainID, systemPrompt, bindingName, artifactDigest string,
	tools []v1alpha1.Tool,
	provider aurora.DispatcherProvider,
) (aurora.Manifest, error) {
	built, err := buildTools(tools, bindingName, artifactDigest, provider)
	if err != nil {
		return aurora.Manifest{}, err
	}
	return aurora.Manifest{
		Version:      aurora.ManifestVersion,
		Brain:        brainID,
		BindingRef:   bindingName,
		SystemPrompt: systemPrompt,
		Tools:        built,
	}, nil
}

func buildTools(tools []v1alpha1.Tool, bindingName, artifactDigest string, provider aurora.DispatcherProvider) ([]aurora.Tool, error) {
	if len(tools) == 0 {
		return nil, nil
	}
	out := make([]aurora.Tool, 0, len(tools))
	for _, t := range tools {
		if t.Type == v1alpha1.AgentToolType {
			settings, err := buildAgentSettings(t, bindingName, artifactDigest)
			if err != nil {
				return nil, fmt.Errorf("agent %q: %w", t.Name, err)
			}
			sub, err := buildTools(t.Tools, bindingName, artifactDigest, provider)
			if err != nil {
				return nil, err
			}
			out = append(out, aurora.Tool{
				Name:     t.Name,
				Type:     t.Type,
				Settings: settings,
				Tools:    sub,
				Hidden:   t.Hidden,
			})
			continue
		}
		resolved := v1alpha1.ResolveLiterals(t.Settings)
		normalized, err := provider.Normalize(t.Type, resolved)
		if err != nil {
			return nil, fmt.Errorf("tool %q (%s): %w", t.Name, t.Type, err)
		}
		out = append(out, aurora.Tool{
			Name:     t.Name,
			Type:     t.Type,
			Settings: normalized,
			Hidden:   t.Hidden,
		})
	}
	return out, nil
}

// buildAgentSettings assembles a core.agent tool's AgentSettings JSON. Code is
// the child's short WASM name qualified by the artifact digest; BindingRef ties
// the child run to the same binding cache as the root for secret warmup.
func buildAgentSettings(t v1alpha1.Tool, bindingName, artifactDigest string) (json.RawMessage, error) {
	code, err := literalString(t.Settings, "code")
	if err != nil {
		return nil, err
	}
	if code == "" {
		return nil, fmt.Errorf("settings.code (child WASM name) is required")
	}
	prompt, err := literalString(t.Settings, "system_prompt")
	if err != nil {
		return nil, err
	}
	onFailure, err := literalString(t.Settings, "on_failure")
	if err != nil {
		return nil, err
	}
	return json.Marshal(aurora.AgentSettings{
		Code:         artifactDigest + "/" + code,
		BindingRef:   bindingName,
		SystemPrompt: prompt,
		OnFailure:    onFailure,
	})
}

// literalString extracts a JSON-string literal setting; a missing key yields "".
func literalString(settings map[string]v1alpha1.SettingValue, key string) (string, error) {
	sv, ok := settings[key]
	if !ok {
		return "", nil
	}
	if sv.Type != v1alpha1.SettingLiteral {
		return "", fmt.Errorf("setting %q must be a literal", key)
	}
	if len(sv.Value) == 0 {
		return "", nil
	}
	var s string
	if err := json.Unmarshal(sv.Value, &s); err != nil {
		return "", fmt.Errorf("setting %q must be a JSON string: %w", key, err)
	}
	return s, nil
}
