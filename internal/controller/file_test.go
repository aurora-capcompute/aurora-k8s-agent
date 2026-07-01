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

	// A YAML Manifest with an inlined brain and a Telegram channel.
	doc := `
apiVersion: aurora.dev/v1alpha1
kind: Manifest
metadata:
  name: ops
spec:
  brain:
    artifact: ghcr.io/acme/brain-k8s:1.4
  channels:
    - kind: TelegramChannel
      name: tg
      telegram:
        botToken: { type: inPlaceEncrypted, ciphertext: AAAA }
        users: ["U1"]
        scopes: ["C1"]
  tools:
    - name: cluster
      type: core.k8s
`
	if err := os.WriteFile(filepath.Join(dir, "a.yaml"), []byte(doc), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	// A non-manifest file that must be ignored.
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("ignore me"), 0o600); err != nil {
		t.Fatalf("write readme: %v", err)
	}

	objs, err := readManifests(dir)
	if err != nil {
		t.Fatalf("readManifests: %v", err)
	}
	if len(objs) != 1 {
		t.Fatalf("objects = %d, want 1 (Manifest)", len(objs))
	}

	in := inputsFromObjects(objs, quietLogger())
	if len(in.Manifests) != 1 {
		t.Fatalf("manifests = %+v", in.Manifests)
	}
	m := in.Manifests[0]
	if m.Name != "ops" || m.Spec.Brain.Artifact != "ghcr.io/acme/brain-k8s:1.4" {
		t.Fatalf("manifest = %+v", m)
	}
	if len(m.Spec.Channels) != 1 || m.Spec.Channels[0].Kind != "TelegramChannel" ||
		m.Spec.Channels[0].Name != "tg" || m.Spec.Channels[0].Telegram == nil ||
		m.Spec.Channels[0].Telegram.BotToken.Type != "inPlaceEncrypted" {
		t.Fatalf("channels = %+v", m.Spec.Channels)
	}
	if len(m.Spec.Tools) != 1 || m.Spec.Tools[0].Name != "cluster" || m.Spec.Tools[0].Type != "core.k8s" {
		t.Fatalf("manifest tools = %+v", m.Spec.Tools)
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
	if len(in.Manifests) != 0 {
		t.Fatalf("unknown kind should produce no inputs, got %+v", in)
	}
}
