package policy

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/aurora-capcompute/aurora-capcompute/aurora"
	"github.com/aurora-capcompute/capcompute/dispatcher"
)

type testProvider struct{}

func (testProvider) Normalize(_ string, settings json.RawMessage) (json.RawMessage, error) {
	if len(settings) == 0 {
		return json.RawMessage(`{}`), nil
	}
	return settings, nil
}

func (testProvider) NewDispatcher(
	context.Context,
	aurora.RunContext,
	aurora.Manifest,
) (dispatcher.Dispatcher[aurora.RunContext], error) {
	return nil, nil
}

func (testProvider) IsSubset(string, json.RawMessage, json.RawMessage) error { return nil }

func TestParseAuthorizesUser(t *testing.T) {
	raw := []byte(`{
	  "version": 1,
	  "users": {
	    "42": {
	      "allowed_chats": [42, -1001],
	      "manifest": {
	        "version": 2,
	        "brain": "kubernetes-agent",
	        "tools": [{"name": "cluster", "type": "core.k8s", "settings": {}}]
	      }
	    }
	  }
	}`)
	set, err := Parse(raw, testProvider{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	user, ok := set.Authorize(42, -1001)
	if !ok {
		t.Fatal("authorized user was rejected")
	}
	if len(user.Manifest.Tools) != 1 {
		t.Fatalf("tools = %d", len(user.Manifest.Tools))
	}
	if _, ok := set.Authorize(42, -1002); ok {
		t.Fatal("unauthorized chat was accepted")
	}
}

func TestParseBindingsFormat(t *testing.T) {
	manifest := `{"version": 2, "brain": "kubernetes-agent", "tools": [{"name": "cluster", "type": "core.k8s", "settings": {}}]}`
	legacy := []byte(`{"version":1,"users":{"42":{"allowed_chats":[-1001],"manifest":` + manifest + `}}}`)
	bindings := []byte(`{"version":2,"manifests":{"ops":` + manifest + `},"bindings":[{"source":"telegram","manifest":"ops","users":["42"],"scopes":["-1001"]},{"source":"slack","manifest":"ops","users":["U9"],"scopes":["C9"]}]}`)

	legacySet, err := Parse(legacy, testProvider{})
	if err != nil {
		t.Fatalf("legacy parse: %v", err)
	}
	bindingSet, err := Parse(bindings, testProvider{})
	if err != nil {
		t.Fatalf("binding parse: %v", err)
	}

	user, ok := bindingSet.Authorize(42, -1001)
	if !ok {
		t.Fatal("bound telegram user was rejected")
	}
	legacyUser, _ := legacySet.Authorize(42, -1001)
	if user.Digest != legacyUser.Digest {
		t.Fatalf("digest mismatch: binding %s != legacy %s", user.Digest, legacyUser.Digest)
	}
	// The slack binding must not leak into the telegram set.
	if _, ok := bindingSet.Authorize(42, -1002); ok {
		t.Fatal("unauthorized chat accepted")
	}
}

func TestParseRejectsInvalid(t *testing.T) {
	cases := []string{
		`{"version":2,"users":{"42":{"allowed_chats":[42],"manifest":{"version":2}}}}`,
		`{"version":1,"users":{"bad":{"allowed_chats":[42],"manifest":{"version":2}}}}`,
		`{"version":1,"users":{"42":{"allowed_chats":[],"manifest":{"version":2}}}}`,
		`{"version":1,"users":{"42":{"allowed_chats":[42],"manifest":{"version":2},"elevation_profiles":{}}}}`,
	}
	for _, raw := range cases {
		if _, err := Parse([]byte(raw), testProvider{}); err == nil {
			t.Fatalf("Parse(%s) succeeded", raw)
		}
	}
}
