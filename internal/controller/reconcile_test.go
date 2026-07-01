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

// telegramChannel builds a ready inline TelegramChannel.
func telegramChannel(name string) v1alpha1.Channel {
	return v1alpha1.Channel{
		Kind: v1alpha1.KindTelegramChannel, Name: name,
		Telegram: &v1alpha1.TelegramChannelSpec{
			BotToken: v1alpha1.SecretSource{Type: v1alpha1.SecretInPlaceEncrypted, Ciphertext: "AAAA"},
			Users:    []string{"42"}, Scopes: []string{"-100"},
		},
	}
}

// manifest builds a Manifest with a brain, one channel, and the named leaf tools.
func manifest(name, artifact string, ch v1alpha1.Channel, tools ...string) NamedManifest {
	built := make([]v1alpha1.Tool, len(tools))
	for i, name := range tools {
		built[i] = v1alpha1.Tool{Name: name, Type: "core.test"}
	}
	return NamedManifest{Name: name, Spec: v1alpha1.ManifestSpec{
		Brain:    v1alpha1.Brain{Artifact: artifact},
		Channels: []v1alpha1.Channel{ch},
		Tools:    built,
	}}
}

func TestReconcileHappyPath(t *testing.T) {
	puller := fakePuller{byRef: map[string]oci.Artifact{
		"ghcr/ops:1": brainArtifact("ops"),
	}}
	in := Inputs{Manifests: []NamedManifest{
		manifest("ops-tg", "ghcr/ops:1", telegramChannel("tg"), "k8s.get"),
	}}
	res := Reconcile(context.Background(), in, puller, testProvider{})

	st := res.ManifestStatus["ops-tg"]
	if !st.Ready || st.BrainID != "sha256:ops/ops" || st.Digest == "" {
		t.Fatalf("manifest status = %+v", st)
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
	// The resolved channel is Manifest-scoped.
	if len(res.Channels) != 1 || res.Channels[0].Name != "ops-tg/tg" {
		t.Fatalf("channels = %+v", res.Channels)
	}
}

func TestReconcileMultiChannel(t *testing.T) {
	// One Manifest serving two channels (telegram + web).
	puller := fakePuller{byRef: map[string]oci.Artifact{
		"ghcr/ops:1": brainArtifact("ops"),
	}}
	in := Inputs{Manifests: []NamedManifest{{Name: "multi", Spec: v1alpha1.ManifestSpec{
		Brain: v1alpha1.Brain{Artifact: "ghcr/ops:1"},
		Channels: []v1alpha1.Channel{
			telegramChannel("tg"),
			{Kind: v1alpha1.KindWebChannel, Name: "web", Web: &v1alpha1.WebChannelSpec{}},
		},
		Tools: []v1alpha1.Tool{{Name: "k8s.get", Type: "core.test"}},
	}}}}
	res := Reconcile(context.Background(), in, puller, testProvider{})
	if !res.ManifestStatus["multi"].Ready {
		t.Fatalf("multi-channel manifest should be Ready: %+v", res.ManifestStatus["multi"])
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
	// A Manifest whose child references a brain not bundled in the artifact.
	puller := fakePuller{byRef: map[string]oci.Artifact{
		"ghcr/ops:1": brainArtifact("ops"),
	}}
	in := Inputs{Manifests: []NamedManifest{{Name: "bad", Spec: v1alpha1.ManifestSpec{
		Brain:        v1alpha1.Brain{Artifact: "ghcr/ops:1"},
		Channels:     []v1alpha1.Channel{telegramChannel("tg")},
		Tools: []v1alpha1.Tool{
			{Name: "k8s.get", Type: "core.test"},
			{Name: "ghost", Type: v1alpha1.AgentToolType, Settings: map[string]v1alpha1.SettingValue{
				"code": {Type: v1alpha1.SettingLiteral, Value: json.RawMessage(`"missing-brain"`)},
			}},
		},
	}}}}
	res := Reconcile(context.Background(), in, puller, testProvider{})
	if res.ManifestStatus["bad"].Ready {
		t.Fatal("manifest referencing an unbundled child brain should not be Ready")
	}
	if res.ManifestStatus["bad"].Message == "" {
		t.Fatal("not-ready manifest should carry a message")
	}
}

func TestReconcileTreeChildren(t *testing.T) {
	// A Manifest declaring a child that runs a bundled brain.
	art := oci.Artifact{
		Main:   "ops",
		Brains: map[string][]byte{"ops": []byte("\x00asm")},
		Digest: "sha256:ops",
	}
	puller := fakePuller{byRef: map[string]oci.Artifact{"ghcr/ops:1": art}}
	in := Inputs{Manifests: []NamedManifest{{Name: "full", Spec: v1alpha1.ManifestSpec{
		Brain:        v1alpha1.Brain{Artifact: "ghcr/ops:1"},
		Channels:     []v1alpha1.Channel{telegramChannel("tg")},
		Tools: []v1alpha1.Tool{
			{Name: "k8s.get", Type: "core.test"},
			{Name: "researcher", Type: v1alpha1.AgentToolType,
				Settings: map[string]v1alpha1.SettingValue{
					"code": {Type: v1alpha1.SettingLiteral, Value: json.RawMessage(`"ops"`)},
				},
				Tools: []v1alpha1.Tool{{Name: "llm.chat", Type: "core.test"}},
			},
		},
	}}}}
	res := Reconcile(context.Background(), in, puller, testProvider{})
	if !res.ManifestStatus["full"].Ready {
		t.Fatalf("manifest with valid children should be Ready: %+v", res.ManifestStatus["full"])
	}
	m := res.Bindings[0].Manifest
	var researcher *aurora.Tool
	for i := range m.Tools {
		if m.Tools[i].Type == aurora.AgentToolType {
			researcher = &m.Tools[i]
		}
	}
	if researcher == nil || researcher.Name != "researcher" {
		t.Fatalf("expected a researcher agent tool, got %+v", m.Tools)
	}
	var as aurora.AgentSettings
	if err := json.Unmarshal(researcher.Settings, &as); err != nil {
		t.Fatalf("decode agent settings: %v", err)
	}
	// Child code is expanded to artifactDigest/brainName.
	if as.Code != "sha256:ops/ops" {
		t.Fatalf("child Code = %q, want sha256:ops/ops", as.Code)
	}
	if as.BindingRef != "full" {
		t.Fatalf("child BindingRef = %q, want full", as.BindingRef)
	}
}

func TestReconcileChannelKinds(t *testing.T) {
	puller := fakePuller{byRef: map[string]oci.Artifact{
		"ghcr/ops:1": brainArtifact("ops"),
	}}
	slack := v1alpha1.Channel{Kind: v1alpha1.KindSlackChannel, Name: "sl", Slack: &v1alpha1.SlackChannelSpec{
		AppToken: v1alpha1.SecretSource{Type: v1alpha1.SecretInPlaceEncrypted, Ciphertext: "A"},
		BotToken: v1alpha1.SecretSource{Type: v1alpha1.SecretInPlaceEncrypted, Ciphertext: "B"},
		Users:    []string{"U1"}, Scopes: []string{"C1"},
	}}
	web := v1alpha1.Channel{Kind: v1alpha1.KindWebChannel, Name: "web", Web: &v1alpha1.WebChannelSpec{}}
	in := Inputs{Manifests: []NamedManifest{
		manifest("on-slack", "ghcr/ops:1", slack, "k8s.get"),
		manifest("on-web", "ghcr/ops:1", web, "k8s.get"),
	}}
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
	badSlack := v1alpha1.Channel{Kind: v1alpha1.KindSlackChannel, Name: "bad", Slack: &v1alpha1.SlackChannelSpec{
		AppToken: v1alpha1.SecretSource{Type: v1alpha1.SecretInPlaceEncrypted, Ciphertext: "A"},
		BotToken: v1alpha1.SecretSource{Type: v1alpha1.SecretInPlaceEncrypted, Ciphertext: "B"},
		// no users/scopes
	}}
	in := Inputs{Manifests: []NamedManifest{
		{Name: "broken", Spec: v1alpha1.ManifestSpec{
			Brain:        v1alpha1.Brain{Artifact: "ghcr/missing:1"},
			Channels:     []v1alpha1.Channel{telegramChannel("tg")},
			Tools: []v1alpha1.Tool{{Name: "k8s.get"}},
		}},
		manifest("bad-chan", "ghcr/ops:1", badSlack, "k8s.get"),
		manifest("ops-tg", "ghcr/ops:1", telegramChannel("tg"), "k8s.get"),
	}}
	res := Reconcile(context.Background(), in, puller, testProvider{})
	if res.ManifestStatus["broken"].Ready || res.ManifestStatus["broken"].Message == "" {
		t.Fatal("manifest with an unpullable brain should be not-ready with a message")
	}
	if res.ManifestStatus["bad-chan"].Ready {
		t.Fatal("manifest with a slack channel missing subjects should be not-ready")
	}
	if !res.ManifestStatus["ops-tg"].Ready {
		t.Fatal("the good manifest should still resolve despite sibling failures")
	}
}

func TestReconcileCapabilitySettingValidADT(t *testing.T) {
	puller := fakePuller{byRef: map[string]oci.Artifact{
		"ghcr/ops:1": brainArtifact("ops"),
	}}
	in := Inputs{Manifests: []NamedManifest{{Name: "b", Spec: v1alpha1.ManifestSpec{
		Brain:    v1alpha1.Brain{Artifact: "ghcr/ops:1"},
		Channels: []v1alpha1.Channel{telegramChannel("tg")},
		Tools: []v1alpha1.Tool{{
			Name: "openai.chat",
			Settings: map[string]v1alpha1.SettingValue{
				"base_url": {Type: v1alpha1.SettingLiteral, Value: json.RawMessage(`"https://api.openai.com/v1"`)},
				"api_key":  {Type: v1alpha1.SecretInPlaceEncrypted, Ciphertext: "AAAA"},
			},
		}},
	}}}}
	res := Reconcile(context.Background(), in, puller, testProvider{})
	if !res.ManifestStatus["b"].Ready {
		t.Fatalf("manifest with valid ADT settings should be Ready: %+v", res.ManifestStatus["b"])
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
	in := Inputs{Manifests: []NamedManifest{{Name: "bad", Spec: v1alpha1.ManifestSpec{
		Brain:    v1alpha1.Brain{Artifact: "ghcr/ops:1"},
		Channels: []v1alpha1.Channel{telegramChannel("tg")},
		Tools: []v1alpha1.Tool{{
			Name:     "openai.chat",
			Settings: map[string]v1alpha1.SettingValue{"api_key": {Type: v1alpha1.SecretInPlaceEncrypted, Ciphertext: ""}},
		}},
	}}}}
	res := Reconcile(context.Background(), in, puller, testProvider{})
	if res.ManifestStatus["bad"].Ready {
		t.Fatal("manifest with empty ciphertext should not be Ready")
	}
	if res.ManifestStatus["bad"].Message == "" {
		t.Fatal("not-ready manifest should carry a message")
	}
	if len(res.Bindings) != 0 {
		t.Fatal("no resolved binding should be produced for a bad setting")
	}
}

func TestReconcileCapabilitySettingUnknownType(t *testing.T) {
	puller := fakePuller{byRef: map[string]oci.Artifact{
		"ghcr/ops:1": brainArtifact("ops"),
	}}
	in := Inputs{Manifests: []NamedManifest{{Name: "bad", Spec: v1alpha1.ManifestSpec{
		Brain:    v1alpha1.Brain{Artifact: "ghcr/ops:1"},
		Channels: []v1alpha1.Channel{telegramChannel("tg")},
		Tools: []v1alpha1.Tool{{
			Name:     "openai.chat",
			Settings: map[string]v1alpha1.SettingValue{"api_key": {Type: "magic"}},
		}},
	}}}}
	res := Reconcile(context.Background(), in, puller, testProvider{})
	if res.ManifestStatus["bad"].Ready {
		t.Fatal("manifest with unknown setting type should not be Ready")
	}
}

func TestReconcileCapabilitySettingDoesNotBlockSiblings(t *testing.T) {
	puller := fakePuller{byRef: map[string]oci.Artifact{
		"ghcr/ops:1": brainArtifact("ops"),
	}}
	in := Inputs{Manifests: []NamedManifest{
		{Name: "bad", Spec: v1alpha1.ManifestSpec{
			Brain:    v1alpha1.Brain{Artifact: "ghcr/ops:1"},
			Channels: []v1alpha1.Channel{telegramChannel("tg")},
			Tools: []v1alpha1.Tool{{
				Name:     "openai.chat",
				Settings: map[string]v1alpha1.SettingValue{"api_key": {Type: v1alpha1.SecretInPlaceEncrypted, Ciphertext: ""}},
			}},
		}},
		manifest("good", "ghcr/ops:1", telegramChannel("tg2"), "openai.chat"),
	}}
	res := Reconcile(context.Background(), in, puller, testProvider{})
	if res.ManifestStatus["bad"].Ready {
		t.Fatal("manifest with bad setting should not be Ready")
	}
	if !res.ManifestStatus["good"].Ready {
		t.Fatalf("good sibling manifest should still be Ready: %+v", res.ManifestStatus["good"])
	}
	if len(res.Bindings) != 1 || res.Bindings[0].Name != "good" {
		t.Fatalf("only the good manifest should be resolved: %+v", res.Bindings)
	}
}

func TestReconcileSystemPromptLiteral(t *testing.T) {
	puller := fakePuller{byRef: map[string]oci.Artifact{
		"ghcr/ops:1": brainArtifact("ops"),
	}}
	in := Inputs{Manifests: []NamedManifest{{Name: "b", Spec: v1alpha1.ManifestSpec{
		Brain:        v1alpha1.Brain{Artifact: "ghcr/ops:1"},
		Channels:     []v1alpha1.Channel{telegramChannel("tg")},
		SystemPrompt: v1alpha1.SettingValue{Type: v1alpha1.SettingLiteral, Value: json.RawMessage(`"You are a helper."`)},
		Tools: []v1alpha1.Tool{{Name: "k8s.get"}},
	}}}}
	res := Reconcile(context.Background(), in, puller, testProvider{})
	if !res.ManifestStatus["b"].Ready {
		t.Fatalf("manifest with literal system prompt should be Ready: %+v", res.ManifestStatus["b"])
	}
	if got := res.Bindings[0].Manifest.SystemPrompt; got != "You are a helper." {
		t.Fatalf("system prompt = %q, want 'You are a helper.'", got)
	}
}

func TestReconcileSystemPromptEncryptedRejected(t *testing.T) {
	puller := fakePuller{byRef: map[string]oci.Artifact{
		"ghcr/ops:1": brainArtifact("ops"),
	}}
	in := Inputs{Manifests: []NamedManifest{{Name: "b", Spec: v1alpha1.ManifestSpec{
		Brain:        v1alpha1.Brain{Artifact: "ghcr/ops:1"},
		Channels:     []v1alpha1.Channel{telegramChannel("tg")},
		SystemPrompt: v1alpha1.SettingValue{Type: v1alpha1.SecretInPlaceEncrypted, Ciphertext: "AAAA"},
		Tools: []v1alpha1.Tool{{Name: "k8s.get"}},
	}}}}
	res := Reconcile(context.Background(), in, puller, testProvider{})
	if res.ManifestStatus["b"].Ready {
		t.Fatal("manifest with encrypted system prompt should not be Ready (not yet supported)")
	}
}
