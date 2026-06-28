package controller

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/aurora-capcompute/aurora-capcompute/aurora"
	"github.com/aurora-capcompute/capcompute/dispatcher"

	"github.com/aurora-capcompute/aurora-k8s-agent/internal/apis/aurora/v1alpha1"
	"github.com/aurora-capcompute/aurora-k8s-agent/internal/oci"
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

// brainArtifact creates a single-brain OCI artifact for tests. The brain ID
// is the short name; the full runtime ID will be "sha256:<id>/<id>".
func brainArtifact(id string) oci.Artifact {
	return oci.Artifact{
		Main:   id,
		Brains: map[string][]byte{id: []byte("\x00asm")},
		Digest: "sha256:" + id,
	}
}

// telegram builds a ready TelegramChannel input.
func telegram(name string) NamedTelegramChannel {
	return NamedTelegramChannel{Name: name, Spec: v1alpha1.TelegramChannelSpec{
		BotToken: v1alpha1.SecretSource{Type: v1alpha1.SecretInPlaceEncrypted, Ciphertext: "AAAA"},
		Users:    []string{"42"}, Scopes: []string{"-100"},
	}}
}

func bind(name, brainRef, chanKind, chanName string, caps ...string) NamedBinding {
	capabilities := make([]v1alpha1.Capability, len(caps))
	for i, c := range caps {
		capabilities[i] = v1alpha1.Capability{Name: c}
	}
	return NamedBinding{Name: name, Spec: v1alpha1.ChannelBindingSpec{
		BrainRef:     brainRef,
		Channels:     []v1alpha1.ChannelRef{{Kind: chanKind, Name: chanName}},
		Capabilities: capabilities,
	}}
}

func TestReconcileHappyPath(t *testing.T) {
	puller := fakePuller{byRef: map[string]oci.Artifact{
		"ghcr/ops:1": brainArtifact("ops"),
	}}
	in := Inputs{
		Brains:           []NamedBrain{{Name: "ops", Spec: v1alpha1.BrainSpec{Artifact: "ghcr/ops:1"}}},
		TelegramChannels: []NamedTelegramChannel{telegram("tg")},
		Bindings:         []NamedBinding{bind("ops-tg", "ops", v1alpha1.KindTelegramChannel, "tg", "k8s.get")},
	}
	res := Reconcile(context.Background(), in, puller, testProvider{})

	// BrainID is now digest/name.
	if !res.BrainStatus["ops"].Ready || res.BrainStatus["ops"].BrainID != "sha256:ops/ops" {
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
	// ResolvedBrain carries the full digest/name ID.
	if len(res.Brains) != 1 || res.Brains[0].ID != "sha256:ops/ops" ||
		string(res.Brains[0].Wasm) != "\x00asm" || res.Brains[0].Digest != "sha256:ops" {
		t.Fatalf("resolved brains = %+v", res.Brains)
	}
	if res.Bindings[0].BindingRef != "ops-tg" {
		t.Fatalf("BindingRef = %q, want ops-tg", res.Bindings[0].BindingRef)
	}
}

func TestReconcileMultiChannel(t *testing.T) {
	// One binding targeting two channels (telegram + web).
	puller := fakePuller{byRef: map[string]oci.Artifact{
		"ghcr/ops:1": brainArtifact("ops"),
	}}
	in := Inputs{
		Brains:           []NamedBrain{{Name: "ops", Spec: v1alpha1.BrainSpec{Artifact: "ghcr/ops:1"}}},
		TelegramChannels: []NamedTelegramChannel{telegram("tg")},
		WebChannels:      []NamedWebChannel{{Name: "web", Spec: v1alpha1.WebChannelSpec{}}},
		Bindings: []NamedBinding{{Name: "multi", Spec: v1alpha1.ChannelBindingSpec{
			BrainRef: "ops",
			Channels: []v1alpha1.ChannelRef{
				{Kind: v1alpha1.KindTelegramChannel, Name: "tg"},
				{Kind: v1alpha1.KindWebChannel, Name: "web"},
			},
			Capabilities: []v1alpha1.Capability{{Name: "k8s.get"}},
		}}},
	}
	res := Reconcile(context.Background(), in, puller, testProvider{})
	if !res.BindingStatus["multi"].Ready {
		t.Fatalf("multi-channel binding should be Ready: %+v", res.BindingStatus["multi"])
	}
	if len(res.Bindings) != 2 {
		t.Fatalf("expected 2 source bindings, got %d: %+v", len(res.Bindings), res.Bindings)
	}
	sources := map[string]bool{}
	for _, b := range res.Bindings {
		sources[b.Source] = true
	}
	if !sources["telegram"] || !sources["web"] {
		t.Fatalf("expected telegram and web sources, got %+v", sources)
	}
	// Both SourceBindings share the same manifest digest.
	if res.Bindings[0].Digest != res.Bindings[1].Digest {
		t.Fatalf("expected same manifest digest for all channels, got %q and %q",
			res.Bindings[0].Digest, res.Bindings[1].Digest)
	}
}

func TestReconcileChildBrainValidation(t *testing.T) {
	// A binding whose child references a brain not bundled in the artifact.
	puller := fakePuller{byRef: map[string]oci.Artifact{
		"ghcr/ops:1": brainArtifact("ops"),
	}}
	in := Inputs{
		Brains:           []NamedBrain{{Name: "ops", Spec: v1alpha1.BrainSpec{Artifact: "ghcr/ops:1"}}},
		TelegramChannels: []NamedTelegramChannel{telegram("tg")},
		Bindings: []NamedBinding{{Name: "bad", Spec: v1alpha1.ChannelBindingSpec{
			BrainRef:     "ops",
			Channels:     []v1alpha1.ChannelRef{{Kind: v1alpha1.KindTelegramChannel, Name: "tg"}},
			Capabilities: []v1alpha1.Capability{{Name: "k8s.get"}},
			Children: []v1alpha1.ChildSpec{{
				Name:  "ghost",
				Brain: "missing-brain",
			}},
		}}},
	}
	res := Reconcile(context.Background(), in, puller, testProvider{})
	if res.BindingStatus["bad"].Ready {
		t.Fatal("binding referencing an unbundled child brain should not be Ready")
	}
	if res.BindingStatus["bad"].Message == "" {
		t.Fatal("not-ready binding should carry a message")
	}
}

func TestReconcileTreeChildren(t *testing.T) {
	// A binding declaring a child that runs a bundled brain.
	art := oci.Artifact{
		Main:   "ops",
		Brains: map[string][]byte{"ops": []byte("\x00asm")},
		Digest: "sha256:ops",
	}
	puller := fakePuller{byRef: map[string]oci.Artifact{"ghcr/ops:1": art}}
	in := Inputs{
		Brains:           []NamedBrain{{Name: "ops", Spec: v1alpha1.BrainSpec{Artifact: "ghcr/ops:1"}}},
		TelegramChannels: []NamedTelegramChannel{telegram("tg")},
		Bindings: []NamedBinding{{Name: "full", Spec: v1alpha1.ChannelBindingSpec{
			BrainRef:     "ops",
			Channels:     []v1alpha1.ChannelRef{{Kind: v1alpha1.KindTelegramChannel, Name: "tg"}},
			Capabilities: []v1alpha1.Capability{{Name: "k8s.get"}},
			Children: []v1alpha1.ChildSpec{{
				Name:         "researcher",
				Brain:        "ops",
				Capabilities: []v1alpha1.Capability{{Name: "llm.chat"}},
			}},
		}}},
	}
	res := Reconcile(context.Background(), in, puller, testProvider{})
	if !res.BindingStatus["full"].Ready {
		t.Fatalf("binding with valid children should be Ready: %+v", res.BindingStatus["full"])
	}
	m := res.Bindings[0].Manifest
	if len(m.Children) != 1 || m.Children[0].Name != "researcher" {
		t.Fatalf("child manifest = %+v", m.Children)
	}
	// Child Brain is expanded to artifactDigest/brainName.
	if m.Children[0].Brain != "sha256:ops/ops" {
		t.Fatalf("child Brain = %q, want sha256:ops/ops", m.Children[0].Brain)
	}
	if m.Children[0].BindingRef != "full" {
		t.Fatalf("child BindingRef = %q, want full", m.Children[0].BindingRef)
	}
}

func TestReconcileChannelKinds(t *testing.T) {
	puller := fakePuller{byRef: map[string]oci.Artifact{
		"ghcr/ops:1": brainArtifact("ops"),
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
		"ghcr/ops:1": brainArtifact("ops"),
	}}
	in := Inputs{
		Brains: []NamedBrain{
			{Name: "ops", Spec: v1alpha1.BrainSpec{Artifact: "ghcr/ops:1"}},
			{Name: "broken", Spec: v1alpha1.BrainSpec{Artifact: "ghcr/missing:1"}},
		},
		TelegramChannels: []NamedTelegramChannel{telegram("tg")},
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

func TestReconcileCapabilitySettingValidADT(t *testing.T) {
	puller := fakePuller{byRef: map[string]oci.Artifact{
		"ghcr/ops:1": brainArtifact("ops"),
	}}
	in := Inputs{
		Brains:           []NamedBrain{{Name: "ops", Spec: v1alpha1.BrainSpec{Artifact: "ghcr/ops:1"}}},
		TelegramChannels: []NamedTelegramChannel{telegram("tg")},
		Bindings: []NamedBinding{{Name: "b", Spec: v1alpha1.ChannelBindingSpec{
			BrainRef: "ops",
			Channels: []v1alpha1.ChannelRef{{Kind: v1alpha1.KindTelegramChannel, Name: "tg"}},
			Capabilities: []v1alpha1.Capability{{
				Name: "openai.chat",
				Settings: map[string]v1alpha1.SettingValue{
					"base_url": {Type: v1alpha1.SettingLiteral, Value: json.RawMessage(`"https://api.openai.com/v1"`)},
					"api_key":  {Type: v1alpha1.SecretInPlaceEncrypted, Ciphertext: "AAAA"},
				},
			}},
		}}},
	}
	res := Reconcile(context.Background(), in, puller, testProvider{})
	if !res.BindingStatus["b"].Ready {
		t.Fatalf("binding with valid ADT settings should be Ready: %+v", res.BindingStatus["b"])
	}
	capSettings := res.Bindings[0].CapabilitySettings
	if capSettings["openai.chat"]["api_key"].Type != v1alpha1.SecretInPlaceEncrypted {
		t.Fatalf("api_key setting not carried through: %+v", capSettings)
	}
}

func TestReconcileCapabilitySettingInvalidSource(t *testing.T) {
	puller := fakePuller{byRef: map[string]oci.Artifact{
		"ghcr/ops:1": brainArtifact("ops"),
	}}
	in := Inputs{
		Brains:           []NamedBrain{{Name: "ops", Spec: v1alpha1.BrainSpec{Artifact: "ghcr/ops:1"}}},
		TelegramChannels: []NamedTelegramChannel{telegram("tg")},
		Bindings: []NamedBinding{{Name: "bad", Spec: v1alpha1.ChannelBindingSpec{
			BrainRef: "ops",
			Channels: []v1alpha1.ChannelRef{{Kind: v1alpha1.KindTelegramChannel, Name: "tg"}},
			Capabilities: []v1alpha1.Capability{{
				Name:     "openai.chat",
				Settings: map[string]v1alpha1.SettingValue{"api_key": {Type: v1alpha1.SecretInPlaceEncrypted, Ciphertext: ""}},
			}},
		}}},
	}
	res := Reconcile(context.Background(), in, puller, testProvider{})
	if res.BindingStatus["bad"].Ready {
		t.Fatal("binding with empty ciphertext should not be Ready")
	}
	if res.BindingStatus["bad"].Message == "" {
		t.Fatal("not-ready binding should carry a message")
	}
	if len(res.Bindings) != 0 {
		t.Fatal("no resolved binding should be produced for a bad setting")
	}
}

func TestReconcileCapabilitySettingUnknownType(t *testing.T) {
	puller := fakePuller{byRef: map[string]oci.Artifact{
		"ghcr/ops:1": brainArtifact("ops"),
	}}
	in := Inputs{
		Brains:           []NamedBrain{{Name: "ops", Spec: v1alpha1.BrainSpec{Artifact: "ghcr/ops:1"}}},
		TelegramChannels: []NamedTelegramChannel{telegram("tg")},
		Bindings: []NamedBinding{{Name: "bad", Spec: v1alpha1.ChannelBindingSpec{
			BrainRef: "ops",
			Channels: []v1alpha1.ChannelRef{{Kind: v1alpha1.KindTelegramChannel, Name: "tg"}},
			Capabilities: []v1alpha1.Capability{{
				Name:     "openai.chat",
				Settings: map[string]v1alpha1.SettingValue{"api_key": {Type: "magic"}},
			}},
		}}},
	}
	res := Reconcile(context.Background(), in, puller, testProvider{})
	if res.BindingStatus["bad"].Ready {
		t.Fatal("binding with unknown setting type should not be Ready")
	}
}

func TestReconcileCapabilitySettingDoesNotBlockSiblings(t *testing.T) {
	puller := fakePuller{byRef: map[string]oci.Artifact{
		"ghcr/ops:1": brainArtifact("ops"),
	}}
	in := Inputs{
		Brains:           []NamedBrain{{Name: "ops", Spec: v1alpha1.BrainSpec{Artifact: "ghcr/ops:1"}}},
		TelegramChannels: []NamedTelegramChannel{telegram("tg")},
		Bindings: []NamedBinding{
			{Name: "bad", Spec: v1alpha1.ChannelBindingSpec{
				BrainRef: "ops",
				Channels: []v1alpha1.ChannelRef{{Kind: v1alpha1.KindTelegramChannel, Name: "tg"}},
				Capabilities: []v1alpha1.Capability{{
					Name:     "openai.chat",
					Settings: map[string]v1alpha1.SettingValue{"api_key": {Type: v1alpha1.SecretInPlaceEncrypted, Ciphertext: ""}},
				}},
			}},
			bind("good", "ops", v1alpha1.KindTelegramChannel, "tg", "openai.chat"),
		},
	}
	res := Reconcile(context.Background(), in, puller, testProvider{})
	if res.BindingStatus["bad"].Ready {
		t.Fatal("binding with bad setting should not be Ready")
	}
	if !res.BindingStatus["good"].Ready {
		t.Fatalf("good sibling binding should still be Ready: %+v", res.BindingStatus["good"])
	}
	if len(res.Bindings) != 1 || res.Bindings[0].Name != "good" {
		t.Fatalf("only the good binding should be resolved: %+v", res.Bindings)
	}
}

func TestReconcileSystemPromptLiteral(t *testing.T) {
	puller := fakePuller{byRef: map[string]oci.Artifact{
		"ghcr/ops:1": brainArtifact("ops"),
	}}
	in := Inputs{
		Brains:           []NamedBrain{{Name: "ops", Spec: v1alpha1.BrainSpec{Artifact: "ghcr/ops:1"}}},
		TelegramChannels: []NamedTelegramChannel{telegram("tg")},
		Bindings: []NamedBinding{{Name: "b", Spec: v1alpha1.ChannelBindingSpec{
			BrainRef:     "ops",
			Channels:     []v1alpha1.ChannelRef{{Kind: v1alpha1.KindTelegramChannel, Name: "tg"}},
			SystemPrompt: v1alpha1.SettingValue{Type: v1alpha1.SettingLiteral, Value: json.RawMessage(`"You are a helper."`)},
			Capabilities: []v1alpha1.Capability{{Name: "k8s.get"}},
		}}},
	}
	res := Reconcile(context.Background(), in, puller, testProvider{})
	if !res.BindingStatus["b"].Ready {
		t.Fatalf("binding with literal system prompt should be Ready: %+v", res.BindingStatus["b"])
	}
	if got := res.Bindings[0].Manifest.SystemPrompt; got != "You are a helper." {
		t.Fatalf("system prompt = %q, want 'You are a helper.'", got)
	}
}

func TestReconcileSystemPromptEncryptedRejected(t *testing.T) {
	puller := fakePuller{byRef: map[string]oci.Artifact{
		"ghcr/ops:1": brainArtifact("ops"),
	}}
	in := Inputs{
		Brains:           []NamedBrain{{Name: "ops", Spec: v1alpha1.BrainSpec{Artifact: "ghcr/ops:1"}}},
		TelegramChannels: []NamedTelegramChannel{telegram("tg")},
		Bindings: []NamedBinding{{Name: "b", Spec: v1alpha1.ChannelBindingSpec{
			BrainRef:     "ops",
			Channels:     []v1alpha1.ChannelRef{{Kind: v1alpha1.KindTelegramChannel, Name: "tg"}},
			SystemPrompt: v1alpha1.SettingValue{Type: v1alpha1.SecretInPlaceEncrypted, Ciphertext: "AAAA"},
			Capabilities: []v1alpha1.Capability{{Name: "k8s.get"}},
		}}},
	}
	res := Reconcile(context.Background(), in, puller, testProvider{})
	if res.BindingStatus["b"].Ready {
		t.Fatal("binding with encrypted system prompt should not be Ready (not yet supported)")
	}
}
