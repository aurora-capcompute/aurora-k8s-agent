package brainspec

import "testing"

func TestParseValid(t *testing.T) {
	raw := []byte(`{
		"id": "kubernetes-agent",
		"capabilities": [
			{"name": "k8s.get"},
			{"name": "k8s.apply", "optional": true},
			{"name": "openai.chat", "settings": {"default_model": "gpt-5.5"}}
		]
	}`)
	m, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if m.ID != "kubernetes-agent" {
		t.Fatalf("id = %q", m.ID)
	}
	if got := m.Required(); len(got) != 2 || got[0] != "k8s.get" || got[1] != "openai.chat" {
		t.Fatalf("required = %v", got)
	}
	if c, ok := m.Declared("k8s.apply"); !ok || !c.Optional {
		t.Fatalf("k8s.apply should be declared optional, got %+v ok=%v", c, ok)
	}
	if _, ok := m.Declared("helm.upgrade"); ok {
		t.Fatal("helm.upgrade should not be declared")
	}
}

func TestParseRejects(t *testing.T) {
	cases := map[string]string{
		"missing id":     `{"capabilities": [{"name": "k8s.get"}]}`,
		"empty cap name": `{"id": "x", "capabilities": [{"name": "  "}]}`,
		"duplicate cap":  `{"id": "x", "capabilities": [{"name": "k8s.get"}, {"name": "k8s.get"}]}`,
		"unknown field":  `{"id": "x", "brain": "y", "capabilities": []}`,
		"malformed json": `{"id": "x"`,
	}
	for name, raw := range cases {
		if _, err := Parse([]byte(raw)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}
