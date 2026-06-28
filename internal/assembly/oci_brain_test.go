package assembly

import (
	"context"
	"errors"
	"testing"

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
		Main:   id,
		Brains: map[string][]byte{id: []byte(wasm)},
		Digest: "sha256:" + id,
	}
}

func TestNewOCIBrainProvider(t *testing.T) {
	puller := fakePuller{byRef: map[string]oci.Artifact{
		"reg/ops:1":  artifact("ops", "\x00asm-ops"),
		"reg/read:1": artifact("readonly", "\x00asm-ro"),
	}}
	p, err := NewOCIBrainProvider(context.Background(), []string{"reg/ops:1", "reg/read:1"}, puller)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	// Default is the main brain of the first artifact.
	if p.DefaultID() != "sha256:ops/ops" {
		t.Fatalf("default = %q, want sha256:ops/ops", p.DefaultID())
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
	// All registered IDs should be digest/name form.
	for _, b := range list {
		if b.ID != "sha256:ops/ops" && b.ID != "sha256:readonly/readonly" {
			t.Fatalf("unexpected brain ID %q", b.ID)
		}
	}
}

func TestNewOCIBrainProviderErrors(t *testing.T) {
	puller := fakePuller{byRef: map[string]oci.Artifact{
		"a": artifact("dup", "1"),
		"b": artifact("dup", "2"),
	}}
	if _, err := NewOCIBrainProvider(context.Background(), nil, puller); err == nil {
		t.Fatal("expected error with no refs")
	}
	// Two artifacts with the same digest/name combo → duplicate error.
	// Use identical wasm so they hash to the same digest.
	sameDigestArtifact := oci.Artifact{
		Main:   "dup",
		Brains: map[string][]byte{"dup": []byte("1")},
		Digest: "sha256:dup",
	}
	dupeArtifact := oci.Artifact{
		Main:   "dup",
		Brains: map[string][]byte{"dup": []byte("2")},
		Digest: "sha256:dup",
	}
	dupPuller := fakePuller{byRef: map[string]oci.Artifact{"a": sameDigestArtifact, "b": dupeArtifact}}
	if _, err := NewOCIBrainProvider(context.Background(), []string{"a", "b"}, dupPuller); err == nil {
		t.Fatal("expected duplicate brain id error")
	}
}
