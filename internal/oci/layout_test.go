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
	config := []byte(`{"abi":1,"main":"kubernetes-agent","brains":["kubernetes-agent"]}`)
	brains := map[string][]byte{"kubernetes-agent": wasm}

	digest, err := WriteLayout(ctx, dir, "", config, brains)
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
	if art.Main != "kubernetes-agent" {
		t.Fatalf("main = %q", art.Main)
	}
	if !bytes.Equal(art.Brains["kubernetes-agent"], wasm) {
		t.Fatalf("wasm mismatch")
	}
	if art.Digest != digest {
		t.Fatalf("digest mismatch: pull=%q write=%q", art.Digest, digest)
	}

	// An explicit tag round-trips too, and packing is idempotent.
	if _, err := WriteLayout(ctx, dir, "v1", config, brains); err != nil {
		t.Fatalf("write tagged layout: %v", err)
	}
	if _, err := NewRemotePuller().Pull(ctx, LayoutScheme+dir+":v1"); err != nil {
		t.Fatalf("pull tagged layout: %v", err)
	}
}

func TestLayoutMultiBrain(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "multi")
	config := []byte(`{"abi":1,"main":"root","brains":["root","scout"]}`)
	brains := map[string][]byte{
		"root":  []byte("\x00asm-root"),
		"scout": []byte("\x00asm-scout"),
	}
	if _, err := WriteLayout(ctx, dir, "", config, brains); err != nil {
		t.Fatalf("write layout: %v", err)
	}
	art, err := NewRemotePuller().Pull(ctx, LayoutScheme+dir)
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if art.Main != "root" || len(art.Brains) != 2 {
		t.Fatalf("main=%q brains=%v", art.Main, art.Brains)
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
