// Package oci pulls Aurora brain artifacts from OCI registries. A brain artifact
// is an OCI image manifest whose config blob is the brain manifest
// (brainspec.Manifest) and which carries the brain wasm as a layer. The pull core
// is transport-agnostic so it can run against a real registry or an in-memory
// store in tests.
package oci

import (
	"context"
	"encoding/json"
	"fmt"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/retry"

	"aurora-k8s-agent/internal/brainspec"
)

// Media types for Aurora brain artifacts.
const (
	ArtifactType         = "application/vnd.aurora.brain.v1+json"
	BrainConfigMediaType = "application/vnd.aurora.brain.config.v1+json"
	BrainWasmMediaType   = "application/vnd.aurora.brain.wasm.v1+wasm"
)

// Artifact is a resolved brain: its declared manifest, the wasm bytes, and the
// manifest digest (for pinning).
type Artifact struct {
	Manifest brainspec.Manifest
	Wasm     []byte
	Digest   string
}

// Puller fetches a brain artifact by OCI reference.
type Puller interface {
	Pull(ctx context.Context, reference string) (Artifact, error)
}

// pull is the transport-agnostic core: it works against any oras read-only
// target (a remote repository, or an in-memory store in tests).
func pull(ctx context.Context, target oras.ReadOnlyTarget, reference string) (Artifact, error) {
	manifestDesc, manifestBytes, err := oras.FetchBytes(ctx, target, reference, oras.DefaultFetchBytesOptions)
	if err != nil {
		return Artifact{}, fmt.Errorf("fetch manifest: %w", err)
	}
	var manifest ocispec.Manifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return Artifact{}, fmt.Errorf("decode manifest: %w", err)
	}

	cfgBytes, err := fetchBlob(ctx, target, manifest.Config)
	if err != nil {
		return Artifact{}, fmt.Errorf("fetch brain config: %w", err)
	}
	spec, err := brainspec.Parse(cfgBytes)
	if err != nil {
		return Artifact{}, err
	}
	if err := spec.CheckABI(); err != nil {
		return Artifact{}, err
	}

	var wasm []byte
	for _, layer := range manifest.Layers {
		if layer.MediaType == BrainWasmMediaType {
			wasm, err = fetchBlob(ctx, target, layer)
			if err != nil {
				return Artifact{}, fmt.Errorf("fetch brain wasm: %w", err)
			}
			break
		}
	}
	if len(wasm) == 0 {
		return Artifact{}, fmt.Errorf("artifact %q has no %s layer", reference, BrainWasmMediaType)
	}
	return Artifact{Manifest: spec, Wasm: wasm, Digest: manifestDesc.Digest.String()}, nil
}

// fetchBlob fetches a descriptor's content, verifying its digest and size.
func fetchBlob(ctx context.Context, fetcher content.Fetcher, desc ocispec.Descriptor) ([]byte, error) {
	rc, err := fetcher.Fetch(ctx, desc)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return content.ReadAll(rc, desc)
}

// RemotePuller pulls brain artifacts from OCI registries.
type RemotePuller struct {
	credential auth.CredentialFunc
	plainHTTP  bool
}

// Option configures a RemotePuller.
type Option func(*RemotePuller)

// WithBasicAuth authenticates to every registry with the given credentials.
func WithBasicAuth(username, password string) Option {
	return func(p *RemotePuller) {
		p.credential = func(context.Context, string) (auth.Credential, error) {
			return auth.Credential{Username: username, Password: password}, nil
		}
	}
}

// WithPlainHTTP uses HTTP instead of HTTPS (for local/dev registries).
func WithPlainHTTP(plain bool) Option {
	return func(p *RemotePuller) { p.plainHTTP = plain }
}

// NewRemotePuller builds a registry-backed puller.
func NewRemotePuller(opts ...Option) *RemotePuller {
	p := &RemotePuller{}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// Pull resolves reference into a brain artifact. A registry reference (e.g.
// ghcr.io/org/brain:tag) is fetched over the network; an "oci-layout:" reference
// is read from an on-disk OCI layout, so a brain can be loaded with no registry.
func (p *RemotePuller) Pull(ctx context.Context, reference string) (Artifact, error) {
	if IsLayoutRef(reference) {
		return pullLayout(ctx, reference)
	}
	repo, err := remote.NewRepository(reference)
	if err != nil {
		return Artifact{}, fmt.Errorf("parse reference %q: %w", reference, err)
	}
	repo.PlainHTTP = p.plainHTTP
	if p.credential != nil {
		repo.Client = &auth.Client{
			Client:     retry.DefaultClient,
			Cache:      auth.NewCache(),
			Credential: p.credential,
		}
	}
	return pull(ctx, repo, reference)
}
