package oci

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/aurora-capcompute/aurora-k8s-agent/internal/brainspec"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/content/memory"
)

// pushBlob stores raw under its content-addressed descriptor and returns it.
func pushBlob(ctx context.Context, t *testing.T, store *memory.Store, mediaType string, raw []byte) ocispec.Descriptor {
	t.Helper()
	desc := content.NewDescriptorFromBytes(mediaType, raw)
	if err := store.Push(ctx, desc, bytes.NewReader(raw)); err != nil {
		t.Fatalf("push %s: %v", mediaType, err)
	}
	return desc
}

// packBrain pushes a brain artifact (config + wasm layer) and tags it.
func packBrain(ctx context.Context, t *testing.T, store *memory.Store, ref string, config, wasm []byte, wasmType string) {
	t.Helper()
	cfg := pushBlob(ctx, t, store, BrainConfigMediaType, config)
	layer := pushBlob(ctx, t, store, wasmType, wasm)
	manifestDesc, err := oras.PackManifest(ctx, store, oras.PackManifestVersion1_1, ArtifactType, oras.PackManifestOptions{
		ConfigDescriptor: &cfg,
		Layers:           []ocispec.Descriptor{layer},
	})
	if err != nil {
		t.Fatalf("pack: %v", err)
	}
	if err := store.Tag(ctx, manifestDesc, ref); err != nil {
		t.Fatalf("tag: %v", err)
	}
}

func TestPullRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	wasm := []byte("\x00asm-brain-bytes")
	config := []byte(`{"id":"kubernetes-agent","abi":1,"capabilities":[{"name":"k8s.get"},{"name":"k8s.apply","optional":true}]}`)
	packBrain(ctx, t, store, "brain:test", config, wasm, BrainWasmMediaType)

	art, err := pull(ctx, store, "brain:test")
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if art.Manifest.ID != "kubernetes-agent" {
		t.Fatalf("id = %q", art.Manifest.ID)
	}
	if !bytes.Equal(art.Wasm, wasm) {
		t.Fatalf("wasm mismatch: %q", art.Wasm)
	}
	if art.Digest == "" {
		t.Fatal("missing manifest digest")
	}
	if c, ok := art.Manifest.Declared("k8s.apply"); !ok || !c.Optional {
		t.Fatalf("k8s.apply optional declaration lost: %+v ok=%v", c, ok)
	}
}

func TestPullRejectsMissingWasmLayer(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	config := []byte(`{"id":"x","abi":1,"capabilities":[]}`)
	// Wrong layer media type → no brain wasm present.
	packBrain(ctx, t, store, "brain:nowasm", config, []byte("data"), "application/octet-stream")

	if _, err := pull(ctx, store, "brain:nowasm"); err == nil {
		t.Fatal("expected error when no brain wasm layer is present")
	}
}

func TestPullRejectsIncompatibleABI(t *testing.T) {
	ctx := context.Background()
	store := memory.New()

	// A brain declaring a different host-call ABI than the host implements must
	// be refused at load.
	future := []byte(`{"id":"future","abi":2,"capabilities":[]}`)
	packBrain(ctx, t, store, "brain:future", future, []byte("\x00asm-future"), BrainWasmMediaType)
	_, err := pull(ctx, store, "brain:future")
	if !errors.Is(err, brainspec.ErrIncompatibleABI) {
		t.Fatalf("future ABI error = %v, want ErrIncompatibleABI", err)
	}

	// An undeclared ABI (0) is likewise refused — a brain must declare it.
	undeclared := []byte(`{"id":"legacy","capabilities":[]}`)
	packBrain(ctx, t, store, "brain:legacy", undeclared, []byte("\x00asm-legacy"), BrainWasmMediaType)
	if _, err := pull(ctx, store, "brain:legacy"); !errors.Is(err, brainspec.ErrIncompatibleABI) {
		t.Fatalf("undeclared ABI error = %v, want ErrIncompatibleABI", err)
	}
}

func TestPullRejectsBadConfig(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	config := []byte(`{"capabilities":[]}`) // missing id
	packBrain(ctx, t, store, "brain:badcfg", config, []byte("\x00asm"), BrainWasmMediaType)

	if _, err := pull(ctx, store, "brain:badcfg"); err == nil {
		t.Fatal("expected error for invalid brain config")
	}
}
