package assembly

import (
	"encoding/json"
	"testing"

	"capcompute/dispatcher"
)

func TestKubernetesSecretCallsAreBlocked(t *testing.T) {
	cases := []dispatcher.Call{
		{Name: "k8s.get", Args: json.RawMessage(`{"kind":"Secret"}`)},
		{Name: "k8s.list", Args: json.RawMessage(`{"kind":"secret"}`)},
		{Name: "k8s.apply", Args: json.RawMessage(`{"resource":{"kind":"Secret"}}`)},
	}
	for _, call := range cases {
		if !isKubernetesSecretCall(call) {
			t.Fatalf("%s was not blocked", call.Name)
		}
	}
	if isKubernetesSecretCall(dispatcher.Call{
		Name: "k8s.get", Args: json.RawMessage(`{"kind":"Deployment"}`),
	}) {
		t.Fatal("Deployment was blocked")
	}
}

func TestVisibleCapabilitiesHidesCognition(t *testing.T) {
	got := visibleCapabilities([]dispatcher.Capability{
		{Name: "openai.chat"},
		{Name: "k8s.get"},
		{Name: "helm.upgrade"},
	})
	if len(got) != 2 || got[0].Name != "k8s.get" || got[1].Name != "helm.upgrade" {
		t.Fatalf("visibleCapabilities = %+v", got)
	}
}
