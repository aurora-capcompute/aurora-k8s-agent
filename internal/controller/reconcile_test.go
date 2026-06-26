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

type testProvider struct {
	// subsetErr, when set, makes IsSubset reject every grant (settings exceed
	// the brain's declared bounds).
	subsetErr error
}

func (testProvider) Normalize(_ string, s json.RawMessage) (json.RawMessage, error) {
	if len(s) == 0 {
		return json.RawMessage(`{}`), nil
	}
	return s, nil
}

func (testProvider) NewDispatcher(context.Context, aurora.RunContext, aurora.Manifest) (dispatcher.Dispatcher[aurora.RunContext], error) {
	return nil, nil
}

func (p testProvider) IsSubset(_ string, _, _ json.RawMessage) error { return p.subsetErr }

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

// telegram builds a ready TelegramChannel input.
func telegram(name string) NamedTelegramChannel {
	return NamedTelegramChannel{Name: name, Spec: v1alpha1.TelegramChannelSpec{
		BotToken: v1alpha1.SecretSource{Type: v1alpha1.SecretInPlaceEncrypted, Ciphertext: "AAAA"},
		Users:    []string{"42"}, Scopes: []string{"-100"},
	}}
}

func bind(name, brainRef, chanKind, chanName string, allowed ...string) NamedBinding {
	caps := make([]v1alpha1.Capability, len(allowed))
	for i, a := range allowed {
		caps[i] = v1alpha1.Capability{Name: a}
	}
	return NamedBinding{Name: name, Spec: v1alpha1.ChannelBindingSpec{
		BrainRef:   brainRef,
		ChannelRef: v1alpha1.ChannelRef{Kind: chanKind, Name: chanName},
		Allowed:    caps,
	}}
}

func TestReconcileHappyPath(t *testing.T) {
	puller := fakePuller{byRef: map[string]oci.Artifact{
		"ghcr/ops:1": brainArtifact("ops",
			brainspec.Capability{Name: "k8s.get"},
			brainspec.Capability{Name: "k8s.apply", Optional: true}),
	}}
	in := Inputs{
		Brains:           []NamedBrain{{Name: "ops", Spec: v1alpha1.BrainSpec{Artifact: "ghcr/ops:1"}}},
		TelegramChannels: []NamedTelegramChannel{telegram("tg")},
		Bindings:         []NamedBinding{bind("ops-tg", "ops", v1alpha1.KindTelegramChannel, "tg", "k8s.get")},
	}
	res := Reconcile(context.Background(), in, puller, testProvider{})

	if !res.BrainStatus["ops"].Ready || res.BrainStatus["ops"].BrainID != "ops" {
		t.Fatalf("brain status = %+v", res.BrainStatus["ops"])
	}
	if !res.ChannelStatus[ChannelKey(v1alpha1.KindTelegramChannel, "tg")].Ready {
		t.Fatalf("channel status = %+v", res.ChannelStatus)
	}
	if !res.BindingStatus["ops-tg"].Ready {
		t.Fatalf("binding status = %+v", res.BindingStatus["ops-tg"])
	}
	if len(res.Bindings) != 1 || res.Bindings[0].Source != "telegram" || res.Bindings[0].Digest == "" {
		t.Fatalf("bindings = %+v", res.Bindings)
	}
	// Users/scopes come from the channel.
	if got := res.Bindings[0].Users; len(got) != 1 || got[0] != "42" {
		t.Fatalf("binding users = %v, want [42] from the channel", got)
	}
	if len(res.BrainRefs) != 1 || res.BrainRefs[0] != "ghcr/ops:1" {
		t.Fatalf("brain refs = %v", res.BrainRefs)
	}
	// The pulled wasm is carried through for live runtime registration, keyed by
	// the brain's declared id.
	if len(res.Brains) != 1 || res.Brains[0].ID != "ops" ||
		string(res.Brains[0].Wasm) != "\x00asm" || res.Brains[0].Digest != "sha256:ops" {
		t.Fatalf("resolved brains = %+v", res.Brains)
	}
}

func TestReconcileRejectsUndeclaredCapability(t *testing.T) {
	puller := fakePuller{byRef: map[string]oci.Artifact{
		"ghcr/ops:1": brainArtifact("ops", brainspec.Capability{Name: "k8s.get"}),
	}}
	in := Inputs{
		Brains:           []NamedBrain{{Name: "ops", Spec: v1alpha1.BrainSpec{Artifact: "ghcr/ops:1"}}},
		TelegramChannels: []NamedTelegramChannel{telegram("tg")},
		Bindings:         []NamedBinding{bind("bad", "ops", v1alpha1.KindTelegramChannel, "tg", "k8s.delete")},
	}
	res := Reconcile(context.Background(), in, puller, testProvider{})
	if res.BindingStatus["bad"].Ready {
		t.Fatal("binding granting an undeclared capability should not be Ready")
	}
	if len(res.Bindings) != 0 {
		t.Fatal("no binding should be produced for an invalid binding")
	}
}

func TestReconcileRejectsMissingRequired(t *testing.T) {
	puller := fakePuller{byRef: map[string]oci.Artifact{
		"ghcr/ops:1": brainArtifact("ops", brainspec.Capability{Name: "k8s.get"}),
	}}
	in := Inputs{
		Brains:           []NamedBrain{{Name: "ops", Spec: v1alpha1.BrainSpec{Artifact: "ghcr/ops:1"}}},
		TelegramChannels: []NamedTelegramChannel{telegram("tg")},
		Bindings:         []NamedBinding{bind("bare", "ops", v1alpha1.KindTelegramChannel, "tg")}, // allowed: []
	}
	res := Reconcile(context.Background(), in, puller, testProvider{})
	if res.BindingStatus["bare"].Ready {
		t.Fatal("binding that omits a required capability should not be Ready")
	}
}

func TestReconcileRejectsSettingsExceedingBounds(t *testing.T) {
	puller := fakePuller{byRef: map[string]oci.Artifact{
		"ghcr/ops:1": brainArtifact("ops", brainspec.Capability{Name: "k8s.get", Settings: json.RawMessage(`{"ns":["a"]}`)}),
	}}
	in := Inputs{
		Brains:           []NamedBrain{{Name: "ops", Spec: v1alpha1.BrainSpec{Artifact: "ghcr/ops:1"}}},
		TelegramChannels: []NamedTelegramChannel{telegram("tg")},
		Bindings: []NamedBinding{{Name: "wide", Spec: v1alpha1.ChannelBindingSpec{
			BrainRef:   "ops",
			ChannelRef: v1alpha1.ChannelRef{Kind: v1alpha1.KindTelegramChannel, Name: "tg"},
			Allowed:    []v1alpha1.Capability{{Name: "k8s.get", Settings: json.RawMessage(`{"ns":["a","b"]}`)}},
		}}},
	}
	res := Reconcile(context.Background(), in, puller, testProvider{subsetErr: errors.New("wider than declared")})
	if res.BindingStatus["wide"].Ready {
		t.Fatal("binding whose settings exceed the brain's declaration should not be Ready")
	}
}

func TestReconcileTreeRequiredUnion(t *testing.T) {
	// The brain tree requires k8s.get at the root and llm.chat at a child; the
	// flat grant must cover both.
	// Root declares llm.chat (optional, so a parent can hold it for delegation);
	// the child requires it, so the tree's required-union is {k8s.get, llm.chat}.
	art := oci.Artifact{
		Manifest: brainspec.Manifest{
			ID: "ops",
			Capabilities: []brainspec.Capability{
				{Name: "k8s.get"},
				{Name: "llm.chat", Optional: true},
			},
			Children: []brainspec.Child{{
				Name:         "researcher",
				Brain:        "ops",
				Capabilities: []brainspec.Capability{{Name: "llm.chat"}},
			}},
		},
		Wasm: []byte("\x00asm"), Digest: "sha256:ops",
	}
	puller := fakePuller{byRef: map[string]oci.Artifact{"ghcr/ops:1": art}}
	base := Inputs{
		Brains:           []NamedBrain{{Name: "ops", Spec: v1alpha1.BrainSpec{Artifact: "ghcr/ops:1"}}},
		TelegramChannels: []NamedTelegramChannel{telegram("tg")},
	}

	// Grant covers only the root requirement → missing the child's → not ready.
	partial := base
	partial.Bindings = []NamedBinding{bind("partial", "ops", v1alpha1.KindTelegramChannel, "tg", "k8s.get")}
	if res := Reconcile(context.Background(), partial, puller, testProvider{}); res.BindingStatus["partial"].Ready {
		t.Fatal("grant missing the child's required capability should not be Ready")
	}

	// Grant covers the whole tree union → ready, and the child carries its cap.
	full := base
	full.Bindings = []NamedBinding{bind("full", "ops", v1alpha1.KindTelegramChannel, "tg", "k8s.get", "llm.chat")}
	res := Reconcile(context.Background(), full, puller, testProvider{})
	if !res.BindingStatus["full"].Ready {
		t.Fatalf("grant covering the tree union should be Ready: %+v", res.BindingStatus["full"])
	}
	m := res.Bindings[0].Manifest
	if len(m.Children) != 1 || len(m.Children[0].Capabilities) != 1 || m.Children[0].Capabilities[0].Name != "llm.chat" {
		t.Fatalf("child manifest = %+v", m.Children)
	}
}

func TestReconcileChannelKinds(t *testing.T) {
	puller := fakePuller{byRef: map[string]oci.Artifact{
		"ghcr/ops:1": brainArtifact("ops", brainspec.Capability{Name: "k8s.get"}),
	}}
	in := Inputs{
		Brains: []NamedBrain{{Name: "ops", Spec: v1alpha1.BrainSpec{Artifact: "ghcr/ops:1"}}},
		SlackChannels: []NamedSlackChannel{{Name: "sl", Spec: v1alpha1.SlackChannelSpec{
			AppToken: v1alpha1.SecretSource{Type: v1alpha1.SecretInPlaceEncrypted, Ciphertext: "A"},
			BotToken: v1alpha1.SecretSource{Type: v1alpha1.SecretInPlaceEncrypted, Ciphertext: "B"},
			Users:    []string{"U1"}, Scopes: []string{"C1"},
		}}},
		WebChannels: []NamedWebChannel{{Name: "web", Spec: v1alpha1.WebChannelSpec{}}},
		Bindings: []NamedBinding{
			bind("on-slack", "ops", v1alpha1.KindSlackChannel, "sl", "k8s.get"),
			bind("on-web", "ops", v1alpha1.KindWebChannel, "web", "k8s.get"),
		},
	}
	res := Reconcile(context.Background(), in, puller, testProvider{})
	sources := map[string]string{}
	for _, b := range res.Bindings {
		sources[b.Name] = b.Source
	}
	if sources["on-slack"] != "slack" || sources["on-web"] != "web" {
		t.Fatalf("sources = %+v", sources)
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
		TelegramChannels: []NamedTelegramChannel{
			telegram("tg"),
			// A Slack channel missing subjects is not ready.
		},
		SlackChannels: []NamedSlackChannel{{Name: "bad", Spec: v1alpha1.SlackChannelSpec{
			AppToken: v1alpha1.SecretSource{Type: v1alpha1.SecretInPlaceEncrypted, Ciphertext: "A"},
			BotToken: v1alpha1.SecretSource{Type: v1alpha1.SecretInPlaceEncrypted, Ciphertext: "B"},
			// no users/scopes
		}}},
		Bindings: []NamedBinding{bind("ops-tg", "ops", v1alpha1.KindTelegramChannel, "tg", "k8s.get")},
	}
	res := Reconcile(context.Background(), in, puller, testProvider{})
	if res.BrainStatus["broken"].Ready || res.BrainStatus["broken"].Message == "" {
		t.Fatal("broken brain should be not-ready with a message")
	}
	if res.ChannelStatus[ChannelKey(v1alpha1.KindSlackChannel, "bad")].Ready {
		t.Fatal("slack channel missing subjects should be not-ready")
	}
	if !res.BindingStatus["ops-tg"].Ready {
		t.Fatal("the good binding should still resolve despite sibling failures")
	}
}
