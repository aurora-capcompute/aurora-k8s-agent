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

// packBrain pushes a brain artifact with annotated WASM layers and tags it.
func packBrain(ctx context.Context, t *testing.T, store *memory.Store, ref string, config []byte, brainWasms map[string][]byte) {
	t.Helper()
	cfg := pushBlob(ctx, t, store, BrainConfigMediaType, config)
	layers := make([]ocispec.Descriptor, 0, len(brainWasms))
	for name, wasm := range brainWasms {
		blobDesc := pushBlob(ctx, t, store, BrainWasmMediaType, wasm)
		blobDesc.Annotations = map[string]string{BrainNameAnnotation: name}
		layers = append(layers, blobDesc)
	}
	manifestDesc, err := oras.PackManifest(ctx, store, oras.PackManifestVersion1_1, ArtifactType, oras.PackManifestOptions{
		ConfigDescriptor: &cfg,
		Layers:           layers,
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
	config := []byte(`{"abi":1,"main":"kubernetes-agent","brains":["kubernetes-agent"]}`)
	packBrain(ctx, t, store, "brain:test", config, map[string][]byte{"kubernetes-agent": wasm})

	art, err := pull(ctx, store, "brain:test")
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if art.Main != "kubernetes-agent" {
		t.Fatalf("main = %q", art.Main)
	}
	if !bytes.Equal(art.Brains["kubernetes-agent"], wasm) {
		t.Fatalf("wasm mismatch")
	}
	if art.Digest == "" {
		t.Fatal("missing manifest digest")
	}
}

func TestPullMultiBrain(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	config := []byte(`{"abi":1,"main":"root","brains":["root","scout"]}`)
	wasms := map[string][]byte{
		"root":  []byte("\x00asm-root"),
		"scout": []byte("\x00asm-scout"),
	}
	packBrain(ctx, t, store, "brain:multi", config, wasms)

	art, err := pull(ctx, store, "brain:multi")
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if art.Main != "root" {
		t.Fatalf("main = %q", art.Main)
	}
	if len(art.Brains) != 2 {
		t.Fatalf("brains = %v", art.Brains)
	}
	if !bytes.Equal(art.Brains["root"], wasms["root"]) || !bytes.Equal(art.Brains["scout"], wasms["scout"]) {
		t.Fatal("wasm content mismatch")
	}
}

func TestPullRejectsMissingWasmLayer(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	config := []byte(`{"abi":1,"main":"x","brains":["x"]}`)
	// Provide no WASM layer at all → pull should error.
	cfg := pushBlob(ctx, t, store, BrainConfigMediaType, config)
	manifestDesc, err := oras.PackManifest(ctx, store, oras.PackManifestVersion1_1, ArtifactType, oras.PackManifestOptions{
		ConfigDescriptor: &cfg,
	})
	if err != nil {
		t.Fatalf("pack: %v", err)
	}
	if err := store.Tag(ctx, manifestDesc, "brain:nowasm"); err != nil {
		t.Fatalf("tag: %v", err)
	}

	if _, err := pull(ctx, store, "brain:nowasm"); err == nil {
		t.Fatal("expected error when no brain wasm layer is present")
	}
}

func TestPullRejectsIncompatibleABI(t *testing.T) {
	ctx := context.Background()
	store := memory.New()

	future := []byte(`{"abi":2,"main":"future","brains":["future"]}`)
	packBrain(ctx, t, store, "brain:future", future, map[string][]byte{"future": []byte("\x00asm-future")})
	_, err := pull(ctx, store, "brain:future")
	if !errors.Is(err, brainspec.ErrIncompatibleABI) {
		t.Fatalf("future ABI error = %v, want ErrIncompatibleABI", err)
	}

	// An undeclared ABI (0) is likewise refused.
	undeclared := []byte(`{"abi":0,"main":"legacy","brains":["legacy"]}`)
	packBrain(ctx, t, store, "brain:legacy", undeclared, map[string][]byte{"legacy": []byte("\x00asm-legacy")})
	if _, err := pull(ctx, store, "brain:legacy"); !errors.Is(err, brainspec.ErrIncompatibleABI) {
		t.Fatalf("undeclared ABI error = %v, want ErrIncompatibleABI", err)
	}
}

func TestPullRejectsBadConfig(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	config := []byte(`{"abi":1}`) // missing main
	packBrain(ctx, t, store, "brain:badcfg", config, map[string][]byte{"x": []byte("\x00asm")})

	if _, err := pull(ctx, store, "brain:badcfg"); err == nil {
		t.Fatal("expected error for invalid brain config")
	}
}
