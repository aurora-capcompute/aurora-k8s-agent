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

	// A multi-document file with a Brain and a TelegramChannel.
	multi := `
apiVersion: aurora.dev/v1alpha1
kind: Brain
metadata:
  name: k8s-brain
spec:
  artifact: ghcr.io/acme/brain-k8s:1.4
---
apiVersion: aurora.dev/v1alpha1
kind: TelegramChannel
metadata:
  name: tg
spec:
  botToken: { type: inPlaceEncrypted, ciphertext: AAAA }
  users: ["U1"]
  scopes: ["C1"]
`
	if err := os.WriteFile(filepath.Join(dir, "a.yaml"), []byte(multi), 0o600); err != nil {
		t.Fatalf("write multi: %v", err)
	}

	// A JSON ChannelBinding in its own file.
	bnd := `{"apiVersion":"aurora.dev/v1alpha1","kind":"ChannelBinding","metadata":{"name":"ops"},` +
		`"spec":{"brainRef":"k8s-brain","channels":[{"kind":"TelegramChannel","name":"tg"}],` +
		`"capabilities":[{"name":"k8s.get"}]}}`
	if err := os.WriteFile(filepath.Join(dir, "b.json"), []byte(bnd), 0o600); err != nil {
		t.Fatalf("write binding: %v", err)
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
		t.Fatalf("objects = %d, want 3 (Brain, TelegramChannel, ChannelBinding)", len(objs))
	}

	in := inputsFromObjects(objs, quietLogger())
	if len(in.Brains) != 1 || in.Brains[0].Name != "k8s-brain" || in.Brains[0].Spec.Artifact != "ghcr.io/acme/brain-k8s:1.4" {
		t.Fatalf("brains = %+v", in.Brains)
	}
	if len(in.TelegramChannels) != 1 || in.TelegramChannels[0].Name != "tg" ||
		in.TelegramChannels[0].Spec.BotToken.Type != "inPlaceEncrypted" {
		t.Fatalf("telegram channels = %+v", in.TelegramChannels)
	}
	if len(in.Bindings) != 1 || in.Bindings[0].Name != "ops" || in.Bindings[0].Spec.BrainRef != "k8s-brain" ||
		in.Bindings[0].Spec.Channels[0].Name != "tg" || in.Bindings[0].Spec.Channels[0].Kind != "TelegramChannel" {
		t.Fatalf("bindings = %+v", in.Bindings)
	}
	if len(in.Bindings[0].Spec.Capabilities) != 1 || in.Bindings[0].Spec.Capabilities[0].Name != "k8s.get" {
		t.Fatalf("binding capabilities = %+v", in.Bindings[0].Spec.Capabilities)
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
	if len(in.Brains)+len(in.SlackChannels)+len(in.TelegramChannels)+len(in.WebChannels)+len(in.Bindings) != 0 {
		t.Fatalf("unknown kind should produce no inputs, got %+v", in)
	}
}
