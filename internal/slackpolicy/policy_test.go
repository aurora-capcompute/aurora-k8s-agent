package slackpolicy

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

const samplePolicy = `{
  "version": 1,
  "users": {
    "U0123": {
      "allowed_channels": ["C0001", "D0002"],
      "manifest": {
        "version": 2,
        "brain": "aurora-agent",
        "capabilities": [{"name": "openai.chat"}]
      }
    }
  }
}`

func TestParseAndAuthorize(t *testing.T) {
	set, err := Parse([]byte(samplePolicy), testProvider{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, ok := set.Authorize("U0123", "C0001"); !ok {
		t.Fatal("user should be authorized in allowed channel")
	}
	if _, ok := set.Authorize("U0123", "C9999"); ok {
		t.Fatal("user should not be authorized in disallowed channel")
	}
	if _, ok := set.Authorize("UZZZZ", "C0001"); ok {
		t.Fatal("unknown user should not be authorized")
	}
}

func TestParseRejectsUserWithoutChannels(t *testing.T) {
	_, err := Parse([]byte(`{"version":1,"users":{"U1":{"allowed_channels":[],"manifest":{"version":2,"brain":"aurora-agent","capabilities":[]}}}}`), testProvider{})
	if err == nil {
		t.Fatal("expected error for user with no channels")
	}
}
