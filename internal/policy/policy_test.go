package policy

import (
	"context"
	"encoding/json"
	"testing"

	"aurora-capcompute/aurora"
	"capcompute/dispatcher"
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
	        "capabilities": [{"name": "k8s.get", "settings": {"namespaces": ["default"]}}]
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
	if len(user.Manifest.Capabilities) != 1 {
		t.Fatalf("capabilities = %d", len(user.Manifest.Capabilities))
	}
	if _, ok := set.Authorize(42, -1002); ok {
		t.Fatal("unauthorized chat was accepted")
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
