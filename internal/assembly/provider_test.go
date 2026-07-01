package assembly

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/aurora-capcompute/aurora-capcompute/aurora"
	"github.com/aurora-capcompute/capcompute/dispatcher"

	"github.com/aurora-capcompute/aurora-k8s-agent/internal/apis/aurora/v1alpha1"
	"github.com/aurora-capcompute/aurora-k8s-agent/internal/binding"
	"github.com/aurora-capcompute/aurora-k8s-agent/internal/secretbox"
	"github.com/aurora-capcompute/aurora-k8s-agent/internal/secrets"
)

func TestKubernetesSecretCallsAreBlocked(t *testing.T) {
	d := &guardedDispatcher{k8sTools: map[string]struct{}{"cluster": {}}}
	cases := []dispatcher.Call{
		{Name: "cluster", Args: json.RawMessage(`{"verb":"get","kind":"Secret"}`)},
		{Name: "cluster", Args: json.RawMessage(`{"verb":"list","kind":"secret"}`)},
		{Name: "cluster", Args: json.RawMessage(`{"verb":"apply","resource":{"kind":"Secret"}}`)},
	}
	for _, call := range cases {
		if !d.isKubernetesSecretCall(call) {
			t.Fatalf("%s was not blocked", call.Args)
		}
	}
	if d.isKubernetesSecretCall(dispatcher.Call{
		Name: "cluster", Args: json.RawMessage(`{"verb":"get","kind":"Deployment"}`),
	}) {
		t.Fatal("Deployment was blocked")
	}
	// A non-k8s tool name is never treated as a secret call.
	if d.isKubernetesSecretCall(dispatcher.Call{Name: "other", Args: json.RawMessage(`{"kind":"Secret"}`)}) {
		t.Fatal("non-k8s tool was blocked")
	}
}

func TestWarmupLiteralSettingsStored(t *testing.T) {
	p := NewProvider()
	b := binding.Resolved{
		BindingRef: "my-binding",
		CapabilitySettings: map[string]map[string]v1alpha1.SettingValue{
			"openai.chat": {
				"base_url": {Type: v1alpha1.SettingLiteral, Value: json.RawMessage(`"https://api.example.com/v1"`)},
				"api_key":  {Type: v1alpha1.SettingLiteral, Value: json.RawMessage(`"sk-test"`)},
			},
		},
	}
	if err := p.Warmup([]binding.Resolved{b}); err != nil {
		t.Fatalf("warmup: %v", err)
	}
	val, ok := p.store.Load("my-binding")
	if !ok {
		t.Fatal("warmup did not store entry")
	}
	entry := val.(bindingEntry)
	var settings map[string]string
	if err := json.Unmarshal(entry.caps["openai.chat"], &settings); err != nil {
		t.Fatalf("unmarshal stored settings: %v", err)
	}
	if settings["base_url"] != "https://api.example.com/v1" || settings["api_key"] != "sk-test" {
		t.Fatalf("stored settings = %+v", settings)
	}
}

func TestWarmupBadCiphertextReturnsError(t *testing.T) {
	const secretKey = "test-key"
	p := NewProvider()
	p.SetResolver(secrets.NewInPlace(secretKey))

	b := binding.Resolved{
		BindingRef: "bad-binding",
		CapabilitySettings: map[string]map[string]v1alpha1.SettingValue{
			"openai.chat": {
				"api_key": {Type: v1alpha1.SecretInPlaceEncrypted, Ciphertext: "notvalidbase64===="},
			},
		},
	}
	if err := p.Warmup([]binding.Resolved{b}); err == nil {
		t.Fatal("warmup with bad ciphertext should return error")
	}
	if _, ok := p.store.Load("bad-binding"); ok {
		t.Fatal("failed warmup must not store entry")
	}
}

func TestWarmupEncryptedSettingResolved(t *testing.T) {
	const secretKey = "warmup-key"
	p := NewProvider()
	p.SetResolver(secrets.NewInPlace(secretKey))

	ct, err := secretbox.SealBase64(secretbox.DeriveKey(secretKey), []byte("sk-real-key"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	b := binding.Resolved{
		BindingRef: "enc-binding",
		CapabilitySettings: map[string]map[string]v1alpha1.SettingValue{
			"openai.chat": {
				"api_key": {Type: v1alpha1.SecretInPlaceEncrypted, Ciphertext: ct},
			},
		},
	}
	if err := p.Warmup([]binding.Resolved{b}); err != nil {
		t.Fatalf("warmup: %v", err)
	}
	val, ok := p.store.Load("enc-binding")
	if !ok {
		t.Fatal("warmup did not store entry")
	}
	entry := val.(bindingEntry)
	var settings map[string]string
	if err := json.Unmarshal(entry.caps["openai.chat"], &settings); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if settings["api_key"] != "sk-real-key" {
		t.Fatalf("resolved api_key = %q, want sk-real-key", settings["api_key"])
	}
}

func TestWarmupTwoBindingsIndependent(t *testing.T) {
	p := NewProvider()
	bindings := []binding.Resolved{
		{
			BindingRef: "binding-a",
			CapabilitySettings: map[string]map[string]v1alpha1.SettingValue{
				"cap": {"key": {Type: v1alpha1.SettingLiteral, Value: json.RawMessage(`"value-a"`)}},
			},
		},
		{
			BindingRef: "binding-b",
			CapabilitySettings: map[string]map[string]v1alpha1.SettingValue{
				"cap": {"key": {Type: v1alpha1.SettingLiteral, Value: json.RawMessage(`"value-b"`)}},
			},
		},
	}
	if err := p.Warmup(bindings); err != nil {
		t.Fatalf("warmup: %v", err)
	}
	for ref, wantVal := range map[string]string{"binding-a": "value-a", "binding-b": "value-b"} {
		val, ok := p.store.Load(ref)
		if !ok {
			t.Fatalf("entry for %q not found", ref)
		}
		entry := val.(bindingEntry)
		var settings map[string]string
		json.Unmarshal(entry.caps["cap"], &settings)
		if settings["key"] != wantVal {
			t.Fatalf("%q key = %q, want %q", ref, settings["key"], wantVal)
		}
	}
}

func TestNewDispatcherMissingWarmupErrors(t *testing.T) {
	p := NewProvider()
	manifest := aurora.Manifest{
		Version:    aurora.ManifestVersion,
		BindingRef: "orphan-binding",
	}
	_, err := p.NewDispatcher(context.Background(), aurora.RunContext{}, manifest)
	if err == nil {
		t.Fatal("NewDispatcher with unwarmed BindingRef should return error")
	}
}

func TestNewDispatcherFileBasedPathSkipsWarmup(t *testing.T) {
	p := NewProvider()
	// Empty BindingRef → file-based fallback, no warmup lookup.
	// With no registrations, Build will fail on an unknown capability, but
	// the error must NOT be a "no warmup entry" error.
	manifest := aurora.Manifest{
		Version: aurora.ManifestVersion,
		Tools:   []aurora.Tool{{Name: "nonexistent", Type: "core.nonexistent"}},
	}
	_, err := p.NewDispatcher(context.Background(), aurora.RunContext{}, manifest)
	if err != nil && err.Error() == `no warmup entry for binding "": call Warmup before dispatching` {
		t.Fatal("file-based path must not require warmup")
	}
}
