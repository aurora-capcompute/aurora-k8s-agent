package oci

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/content/oci"
)

// LayoutScheme marks a brain reference that resolves to an on-disk OCI image
// layout directory rather than a registry. It lets a brain be pulled with no
// registry at all — the layout can be built locally (see WriteLayout / the
// `pack-brain` subcommand) and mounted or baked into an image.
const LayoutScheme = "oci-layout:"

// defaultLayoutTag is used when a layout reference omits a tag.
const defaultLayoutTag = "latest"

// IsLayoutRef reports whether a reference targets an on-disk OCI layout.
func IsLayoutRef(reference string) bool {
	return strings.HasPrefix(reference, LayoutScheme)
}

// parseLayoutRef splits "oci-layout:<dir>:<tag>" into its directory and tag.
// The tag is whatever follows the last colon; if there is none, it defaults to
// "latest" (so "oci-layout:/srv/brain" is "/srv/brain" at :latest).
func parseLayoutRef(reference string) (dir, tag string) {
	rest := strings.TrimPrefix(reference, LayoutScheme)
	if i := strings.LastIndex(rest, ":"); i >= 0 {
		return rest[:i], rest[i+1:]
	}
	return rest, defaultLayoutTag
}

// pullLayout resolves a brain artifact from an on-disk OCI layout, reusing the
// same manifest/config/wasm validation as a registry pull.
func pullLayout(ctx context.Context, reference string) (Artifact, error) {
	dir, tag := parseLayoutRef(reference)
	store, err := oci.New(dir)
	if err != nil {
		return Artifact{}, fmt.Errorf("open OCI layout %q: %w", dir, err)
	}
	return pull(ctx, store, tag)
}

// WriteLayout packs a brain (its declared manifest plus wasm) into an OCI image
// layout directory at dir, tagged with tag (default "latest"). The result is a
// registry-less artifact that pullLayout — and therefore the brain provider and
// control plane — can consume via an "oci-layout:" reference. configJSON must be
// a valid brainspec manifest; it is stored verbatim as the artifact config.
func WriteLayout(ctx context.Context, dir, tag string, configJSON, wasm []byte) (string, error) {
	if tag == "" {
		tag = defaultLayoutTag
	}
	store, err := oci.New(dir)
	if err != nil {
		return "", fmt.Errorf("create OCI layout %q: %w", dir, err)
	}

	cfgDesc := content.NewDescriptorFromBytes(BrainConfigMediaType, configJSON)
	if err := pushIfAbsent(ctx, store, cfgDesc, configJSON); err != nil {
		return "", fmt.Errorf("push brain config: %w", err)
	}
	wasmDesc := content.NewDescriptorFromBytes(BrainWasmMediaType, wasm)
	if err := pushIfAbsent(ctx, store, wasmDesc, wasm); err != nil {
		return "", fmt.Errorf("push brain wasm: %w", err)
	}

	manifestDesc, err := oras.PackManifest(ctx, store, oras.PackManifestVersion1_1, ArtifactType,
		oras.PackManifestOptions{ConfigDescriptor: &cfgDesc, Layers: []ocispec.Descriptor{wasmDesc}})
	if err != nil {
		return "", fmt.Errorf("pack brain manifest: %w", err)
	}
	if err := store.Tag(ctx, manifestDesc, tag); err != nil {
		return "", fmt.Errorf("tag brain manifest: %w", err)
	}
	return manifestDesc.Digest.String(), nil
}

// pushIfAbsent pushes content unless the layout already holds it, so packing is
// idempotent across re-runs.
func pushIfAbsent(ctx context.Context, store *oci.Store, desc ocispec.Descriptor, data []byte) error {
	if ok, err := store.Exists(ctx, desc); err != nil {
		return err
	} else if ok {
		return nil
	}
	return store.Push(ctx, desc, bytes.NewReader(data))
}
