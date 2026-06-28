package oci

import (
	"bytes"
	"context"
	"fmt"
	"sort"
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

// WriteLayout packs a multi-brain bundle into an OCI image layout at dir, tagged
// with tag (default "latest"). configJSON is the brainspec manifest stored as the
// artifact config. brains maps each brain's short name to its WASM bytes; each
// brain becomes one annotated layer. Brain names are sorted for determinism.
func WriteLayout(ctx context.Context, dir, tag string, configJSON []byte, brains map[string][]byte) (string, error) {
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

	names := make([]string, 0, len(brains))
	for name := range brains {
		names = append(names, name)
	}
	sort.Strings(names)

	layers := make([]ocispec.Descriptor, 0, len(names))
	for _, name := range names {
		wasm := brains[name]
		blobDesc := content.NewDescriptorFromBytes(BrainWasmMediaType, wasm)
		if err := pushIfAbsent(ctx, store, blobDesc, wasm); err != nil {
			return "", fmt.Errorf("push brain wasm %q: %w", name, err)
		}
		layerDesc := blobDesc
		layerDesc.Annotations = map[string]string{BrainNameAnnotation: name}
		layers = append(layers, layerDesc)
	}

	manifestDesc, err := oras.PackManifest(ctx, store, oras.PackManifestVersion1_1, ArtifactType,
		oras.PackManifestOptions{ConfigDescriptor: &cfgDesc, Layers: layers})
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
