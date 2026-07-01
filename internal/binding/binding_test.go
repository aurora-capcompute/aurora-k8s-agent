package binding

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/aurora-capcompute/aurora-capcompute/aurora"
	"github.com/aurora-capcompute/capcompute/dispatcher"
)

type testProvider struct{}

func (testProvider) Normalize(_ string, settings json.RawMessage) (json.RawMessage, error) {
	if len(settings) == 0 {
		return json.RawMessage(`{}`), nil
	}
	return settings, nil
}

func (testProvider) NewDispatcher(
	context.Context,
	aurora.RunContext,
	aurora.Manifest,
) (dispatcher.Dispatcher[aurora.RunContext], error) {
	return nil, nil
}

func (testProvider) IsSubset(string, json.RawMessage, json.RawMessage) error { return nil }

const sample = `{
  "version": 2,
  "manifests": {
    "ops": {"version": 2, "brain": "kubernetes-agent", "tools": [{"name": "llm", "type": "core.test"}]},
    "readonly": {"version": 2, "brain": "kubernetes-agent", "tools": [{"name": "llm", "type": "core.test"}]}
  },
  "bindings": [
    {"source": "telegram", "manifest": "ops", "users": ["123", "456"], "scopes": ["-100999"]},
    {"source": "slack", "manifest": "readonly", "users": ["U1"], "scopes": ["C1", "D2"]}
  ]
}`

func TestIsBindingFormat(t *testing.T) {
	if !IsBindingFormat([]byte(sample)) {
		t.Fatal("sample should be detected as binding format")
	}
	legacy := `{"version":1,"users":{"1":{"allowed_chats":[2],"manifest":{"version":2}}}}`
	if IsBindingFormat([]byte(legacy)) {
		t.Fatal("legacy users format should not be flagged as bindings")
	}
}

func TestForSourceFiltersAndResolves(t *testing.T) {
	tg, err := ForSource([]byte(sample), "telegram", testProvider{})
	if err != nil {
		t.Fatalf("telegram: %v", err)
	}
	if len(tg) != 1 || len(tg[0].Users) != 2 || tg[0].Users[0] != "123" {
		t.Fatalf("unexpected telegram bindings: %+v", tg)
	}
	if tg[0].Digest == "" {
		t.Fatal("telegram binding missing digest")
	}

	sl, err := ForSource([]byte(sample), "slack", testProvider{})
	if err != nil {
		t.Fatalf("slack: %v", err)
	}
	if len(sl) != 1 || sl[0].Users[0] != "U1" || len(sl[0].Scopes) != 2 {
		t.Fatalf("unexpected slack bindings: %+v", sl)
	}

	// A source with no bindings resolves to nothing, not an error.
	none, err := ForSource([]byte(sample), "discord", testProvider{})
	if err != nil || none != nil {
		t.Fatalf("unknown source = (%v, %v)", none, err)
	}
}

func TestForSourceRejectsInvalid(t *testing.T) {
	cases := map[string]string{
		"unknown manifest": `{"version":2,"manifests":{"ops":{"version":2,"brain":"x","capabilities":[{"name":"openai.chat"}]}},"bindings":[{"source":"telegram","manifest":"nope","users":["1"],"scopes":["2"]}]}`,
		"wrong version":    `{"version":1,"manifests":{"ops":{"version":2}},"bindings":[]}`,
		"no manifests":     `{"version":2,"manifests":{},"bindings":[]}`,
		"no users":         `{"version":2,"manifests":{"ops":{"version":2,"brain":"x","capabilities":[{"name":"openai.chat"}]}},"bindings":[{"source":"telegram","manifest":"ops","users":[],"scopes":["2"]}]}`,
		"no scopes":        `{"version":2,"manifests":{"ops":{"version":2,"brain":"x","capabilities":[{"name":"openai.chat"}]}},"bindings":[{"source":"telegram","manifest":"ops","users":["1"],"scopes":[]}]}`,
	}
	for name, raw := range cases {
		if _, err := ForSource([]byte(raw), "telegram", testProvider{}); err == nil {
			t.Fatalf("%s: expected error", name)
		}
	}
}
