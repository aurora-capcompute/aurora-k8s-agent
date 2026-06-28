package brainspec

import "testing"

func TestParseValid(t *testing.T) {
	// Multi-brain manifest.
	raw := []byte(`{"abi":1,"main":"kubernetes-agent","brains":["kubernetes-agent","k8s-scout"]}`)
	m, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if m.Main != "kubernetes-agent" {
		t.Fatalf("main = %q", m.Main)
	}
	if len(m.Brains) != 2 {
		t.Fatalf("brains = %v", m.Brains)
	}
	if err := m.CheckABI(); err != nil {
		t.Fatalf("CheckABI: %v", err)
	}
}

func TestParseDefaultsBrains(t *testing.T) {
	// When 'brains' is omitted, it defaults to [main].
	raw := []byte(`{"abi":1,"main":"kubernetes-agent"}`)
	m, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(m.Brains) != 1 || m.Brains[0] != "kubernetes-agent" {
		t.Fatalf("brains = %v, want [kubernetes-agent]", m.Brains)
	}
}

func TestParseRejects(t *testing.T) {
	cases := map[string]string{
		"missing main":       `{"abi":1,"brains":["x"]}`,
		"main not in brains": `{"abi":1,"main":"a","brains":["b"]}`,
		"empty brain name":   `{"abi":1,"main":"a","brains":["a",""]}`,
		"unknown field":      `{"abi":1,"main":"x","extra":"y"}`,
		"malformed json":     `{"main":"x"`,
	}
	for name, raw := range cases {
		if _, err := Parse([]byte(raw)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestCheckABIRejectsIncompatible(t *testing.T) {
	m := Manifest{ABI: 2, Main: "x", Brains: []string{"x"}}
	if err := m.CheckABI(); err == nil {
		t.Fatal("expected ErrIncompatibleABI for ABI=2")
	}
}
