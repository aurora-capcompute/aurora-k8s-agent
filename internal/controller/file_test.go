package controller

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestReadManifestsAndBuildInputs(t *testing.T) {
	dir := t.TempDir()

	// A multi-document file with a Brain and a Channel.
	multi := `
apiVersion: aurora.dev/v1alpha1
kind: Brain
metadata:
  name: k8s-brain
spec:
  artifact: ghcr.io/acme/brain-k8s:1.4
---
apiVersion: aurora.dev/v1alpha1
kind: Channel
metadata:
  name: tg
spec:
  source: telegram
  secretRef: tg-secret
`
	if err := os.WriteFile(filepath.Join(dir, "a.yaml"), []byte(multi), 0o600); err != nil {
		t.Fatalf("write multi: %v", err)
	}

	// A JSON FunctionInstance in its own file.
	instance := `{"apiVersion":"aurora.dev/v1alpha1","kind":"FunctionInstance","metadata":{"name":"ops"},` +
		`"spec":{"brainRef":"k8s-brain","channelRef":"tg","capabilities":[{"name":"k8s.get"}],` +
		`"subjects":{"users":["U1"],"scopes":["C1"]}}}`
	if err := os.WriteFile(filepath.Join(dir, "b.json"), []byte(instance), 0o600); err != nil {
		t.Fatalf("write instance: %v", err)
	}

	// A non-manifest file that must be ignored.
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("ignore me"), 0o600); err != nil {
		t.Fatalf("write readme: %v", err)
	}

	objs, err := readManifests(dir)
	if err != nil {
		t.Fatalf("readManifests: %v", err)
	}
	if len(objs) != 3 {
		t.Fatalf("objects = %d, want 3 (Brain, Channel, FunctionInstance)", len(objs))
	}

	in := inputsFromObjects(objs, quietLogger())
	if len(in.Brains) != 1 || in.Brains[0].Name != "k8s-brain" || in.Brains[0].Spec.Artifact != "ghcr.io/acme/brain-k8s:1.4" {
		t.Fatalf("brains = %+v", in.Brains)
	}
	if len(in.Channels) != 1 || in.Channels[0].Name != "tg" || in.Channels[0].Spec.Source != "telegram" {
		t.Fatalf("channels = %+v", in.Channels)
	}
	if len(in.Instances) != 1 || in.Instances[0].Name != "ops" || in.Instances[0].Spec.BrainRef != "k8s-brain" || in.Instances[0].Spec.ChannelRef != "tg" {
		t.Fatalf("instances = %+v", in.Instances)
	}
	if len(in.Instances[0].Spec.Capabilities) != 1 || in.Instances[0].Spec.Capabilities[0].Name != "k8s.get" {
		t.Fatalf("instance capabilities = %+v", in.Instances[0].Spec.Capabilities)
	}
}

func TestReadManifestsMissingDir(t *testing.T) {
	if _, err := readManifests(filepath.Join(t.TempDir(), "does-not-exist")); err == nil {
		t.Fatal("expected error for missing directory")
	}
}

func TestInputsFromObjectsSkipsUnknownKinds(t *testing.T) {
	dir := t.TempDir()
	doc := `
apiVersion: v1
kind: ConfigMap
metadata:
  name: stray
data:
  k: v
`
	if err := os.WriteFile(filepath.Join(dir, "c.yaml"), []byte(doc), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	objs, err := readManifests(dir)
	if err != nil {
		t.Fatalf("readManifests: %v", err)
	}
	in := inputsFromObjects(objs, quietLogger())
	if len(in.Brains)+len(in.Channels)+len(in.Instances) != 0 {
		t.Fatalf("unknown kind should produce no inputs, got %+v", in)
	}
}
