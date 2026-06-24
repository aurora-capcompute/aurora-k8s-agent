package controller

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"aurora-capcompute/aurora"
	"capcompute/dispatcher"

	"aurora-k8s-agent/internal/apis/aurora/v1alpha1"
	"aurora-k8s-agent/internal/brainspec"
	"aurora-k8s-agent/internal/oci"
)

type testProvider struct{}

func (testProvider) Normalize(_ string, s json.RawMessage) (json.RawMessage, error) {
	if len(s) == 0 {
		return json.RawMessage(`{}`), nil
	}
	return s, nil
}

func (testProvider) NewDispatcher(context.Context, aurora.RunContext, aurora.Manifest) (dispatcher.Dispatcher[aurora.RunContext], error) {
	return nil, nil
}

func (testProvider) IsSubset(_ string, _, _ json.RawMessage) error { return nil }

type fakePuller struct{ byRef map[string]oci.Artifact }

func (f fakePuller) Pull(_ context.Context, ref string) (oci.Artifact, error) {
	a, ok := f.byRef[ref]
	if !ok {
		return oci.Artifact{}, errors.New("not found")
	}
	return a, nil
}

func brainArtifact(id string, caps ...brainspec.Capability) oci.Artifact {
	return oci.Artifact{Manifest: brainspec.Manifest{ID: id, Capabilities: caps}, Wasm: []byte("\x00asm"), Digest: "sha256:" + id}
}

func TestReconcileHappyPath(t *testing.T) {
	puller := fakePuller{byRef: map[string]oci.Artifact{
		"ghcr/ops:1": brainArtifact("ops", brainspec.Capability{Name: "k8s.get"}, brainspec.Capability{Name: "k8s.apply", Optional: true}),
	}}
	in := Inputs{
		Brains:   []NamedBrain{{Name: "ops", Spec: v1alpha1.BrainSpec{Artifact: "ghcr/ops:1"}}},
		Channels: []NamedChannel{{Name: "tg", Spec: v1alpha1.ChannelSpec{Source: "telegram", SecretRef: "tg-secret"}}},
		Instances: []NamedInstance{{Name: "ops-tg", Spec: v1alpha1.FunctionInstanceSpec{
			BrainRef:     "ops",
			ChannelRef:   "tg",
			Capabilities: []v1alpha1.Capability{{Name: "k8s.get"}},
			Subjects:     v1alpha1.Subjects{Users: []string{"42"}, Scopes: []string{"-100"}},
		}}},
	}
	res := Reconcile(context.Background(), in, puller, testProvider{})

	if !res.BrainStatus["ops"].Ready || res.BrainStatus["ops"].BrainID != "ops" {
		t.Fatalf("brain status = %+v", res.BrainStatus["ops"])
	}
	if !res.ChannelStatus["tg"].Ready {
		t.Fatalf("channel status = %+v", res.ChannelStatus["tg"])
	}
	if !res.InstanceStatus["ops-tg"].Ready {
		t.Fatalf("instance status = %+v", res.InstanceStatus["ops-tg"])
	}
	if len(res.Bindings) != 1 || res.Bindings[0].Source != "telegram" || res.Bindings[0].Digest == "" {
		t.Fatalf("bindings = %+v", res.Bindings)
	}
	if len(res.BrainRefs) != 1 || res.BrainRefs[0] != "ghcr/ops:1" {
		t.Fatalf("brain refs = %v", res.BrainRefs)
	}
}

func TestReconcileRejectsUndeclaredCapability(t *testing.T) {
	puller := fakePuller{byRef: map[string]oci.Artifact{
		"ghcr/ops:1": brainArtifact("ops", brainspec.Capability{Name: "k8s.get"}),
	}}
	in := Inputs{
		Brains:   []NamedBrain{{Name: "ops", Spec: v1alpha1.BrainSpec{Artifact: "ghcr/ops:1"}}},
		Channels: []NamedChannel{{Name: "tg", Spec: v1alpha1.ChannelSpec{Source: "telegram", SecretRef: "s"}}},
		Instances: []NamedInstance{{Name: "bad", Spec: v1alpha1.FunctionInstanceSpec{
			BrainRef: "ops", ChannelRef: "tg",
			Capabilities: []v1alpha1.Capability{{Name: "k8s.delete"}},
			Subjects:     v1alpha1.Subjects{Users: []string{"1"}, Scopes: []string{"2"}},
		}}},
	}
	res := Reconcile(context.Background(), in, puller, testProvider{})
	if res.InstanceStatus["bad"].Ready {
		t.Fatal("instance granting an undeclared capability should not be Ready")
	}
	if len(res.Bindings) != 0 {
		t.Fatal("no binding should be produced for an invalid instance")
	}
}

func TestReconcileIsolatesFailures(t *testing.T) {
	puller := fakePuller{byRef: map[string]oci.Artifact{
		"ghcr/ops:1": brainArtifact("ops", brainspec.Capability{Name: "k8s.get"}),
	}}
	in := Inputs{
		Brains: []NamedBrain{
			{Name: "ops", Spec: v1alpha1.BrainSpec{Artifact: "ghcr/ops:1"}},
			{Name: "broken", Spec: v1alpha1.BrainSpec{Artifact: "ghcr/missing:1"}},
		},
		Channels: []NamedChannel{
			{Name: "tg", Spec: v1alpha1.ChannelSpec{Source: "telegram", SecretRef: "s"}},
			{Name: "bad", Spec: v1alpha1.ChannelSpec{Source: "irc", SecretRef: "s"}},
		},
		Instances: []NamedInstance{{Name: "ops-tg", Spec: v1alpha1.FunctionInstanceSpec{
			BrainRef: "ops", ChannelRef: "tg",
			Capabilities: []v1alpha1.Capability{{Name: "k8s.get"}},
			Subjects:     v1alpha1.Subjects{Users: []string{"1"}, Scopes: []string{"2"}},
		}}},
	}
	res := Reconcile(context.Background(), in, puller, testProvider{})
	if res.BrainStatus["broken"].Ready || res.BrainStatus["broken"].Message == "" {
		t.Fatal("broken brain should be not-ready with a message")
	}
	if res.ChannelStatus["bad"].Ready {
		t.Fatal("unsupported channel source should be not-ready")
	}
	if !res.InstanceStatus["ops-tg"].Ready {
		t.Fatal("the good instance should still resolve despite sibling failures")
	}
}
