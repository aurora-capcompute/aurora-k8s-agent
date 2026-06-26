package oci

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
)

// TestLayoutRoundTrip packs a brain into an on-disk OCI layout and pulls it back
// through the public RemotePuller via an "oci-layout:" reference — the
// registry-less path the control plane and brain provider use.
func TestLayoutRoundTrip(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "layout")
	wasm := []byte("\x00asm-layout-bytes")
	config := []byte(`{"id":"kubernetes-agent","abi":1,"capabilities":[{"name":"k8s.get"}]}`)

	digest, err := WriteLayout(ctx, dir, "", config, wasm)
	if err != nil {
		t.Fatalf("write layout: %v", err)
	}
	if digest == "" {
		t.Fatal("WriteLayout returned empty digest")
	}

	// Default tag is "latest"; pull through the same puller the agent uses.
	art, err := NewRemotePuller().Pull(ctx, LayoutScheme+dir)
	if err != nil {
		t.Fatalf("pull layout: %v", err)
	}
	if art.Manifest.ID != "kubernetes-agent" {
		t.Fatalf("id = %q", art.Manifest.ID)
	}
	if !bytes.Equal(art.Wasm, wasm) {
		t.Fatalf("wasm mismatch: %q", art.Wasm)
	}
	if art.Digest != digest {
		t.Fatalf("digest mismatch: pull=%q write=%q", art.Digest, digest)
	}

	// An explicit tag round-trips too, and packing is idempotent.
	if _, err := WriteLayout(ctx, dir, "v1", config, wasm); err != nil {
		t.Fatalf("write tagged layout: %v", err)
	}
	if _, err := NewRemotePuller().Pull(ctx, LayoutScheme+dir+":v1"); err != nil {
		t.Fatalf("pull tagged layout: %v", err)
	}
}

func TestParseLayoutRef(t *testing.T) {
	cases := []struct{ ref, dir, tag string }{
		{LayoutScheme + "/srv/brain", "/srv/brain", "latest"},
		{LayoutScheme + "/srv/brain:v2", "/srv/brain", "v2"},
		{LayoutScheme + "rel/path:latest", "rel/path", "latest"},
	}
	for _, c := range cases {
		dir, tag := parseLayoutRef(c.ref)
		if dir != c.dir || tag != c.tag {
			t.Errorf("parseLayoutRef(%q) = (%q,%q), want (%q,%q)", c.ref, dir, tag, c.dir, c.tag)
		}
	}
}
