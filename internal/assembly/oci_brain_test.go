package assembly

import (
	"context"
	"errors"
	"testing"

	"github.com/aurora-capcompute/aurora-k8s-agent/internal/brainspec"
	"github.com/aurora-capcompute/aurora-k8s-agent/internal/oci"
)

type fakePuller struct {
	byRef map[string]oci.Artifact
}

func (f fakePuller) Pull(_ context.Context, ref string) (oci.Artifact, error) {
	art, ok := f.byRef[ref]
	if !ok {
		return oci.Artifact{}, errors.New("not found: " + ref)
	}
	return art, nil
}

func artifact(id string, wasm string) oci.Artifact {
	return oci.Artifact{
		Manifest: brainspec.Manifest{ID: id, Capabilities: []brainspec.Capability{{Name: "k8s.get"}}},
		Wasm:     []byte(wasm),
		Digest:   "sha256:" + id,
	}
}

func TestNewOCIBrainProvider(t *testing.T) {
	puller := fakePuller{byRef: map[string]oci.Artifact{
		"reg/ops:1":  artifact("ops", "\x00asm-ops"),
		"reg/read:1": artifact("readonly", "\x00asm-ro"),
	}}
	p, err := NewOCIBrainProvider(context.Background(), []string{"reg/ops:1", "reg/read:1"}, "readonly", puller)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if p.DefaultID() != "readonly" {
		t.Fatalf("default = %q", p.DefaultID())
	}
	list, err := p.List(context.Background())
	if err != nil || len(list) != 2 {
		t.Fatalf("list = %v (err %v)", list, err)
	}
	// Mutating the returned wasm must not corrupt the provider's copy.
	list[0].Wasm[0] = 0xff
	again, _ := p.List(context.Background())
	if again[0].Wasm[0] == 0xff {
		t.Fatal("List must return defensive copies")
	}
	if _, ok := p.Declarations()["ops"]; !ok {
		t.Fatal("declarations should include ops")
	}
}

func TestNewOCIBrainProviderErrors(t *testing.T) {
	puller := fakePuller{byRef: map[string]oci.Artifact{
		"a": artifact("dup", "1"),
		"b": artifact("dup", "2"),
	}}
	if _, err := NewOCIBrainProvider(context.Background(), nil, "", puller); err == nil {
		t.Fatal("expected error with no refs")
	}
	if _, err := NewOCIBrainProvider(context.Background(), []string{"a", "b"}, "", puller); err == nil {
		t.Fatal("expected duplicate brain id error")
	}
	if _, err := NewOCIBrainProvider(context.Background(), []string{"a"}, "missing", puller); err == nil {
		t.Fatal("expected unknown default brain error")
	}
}
