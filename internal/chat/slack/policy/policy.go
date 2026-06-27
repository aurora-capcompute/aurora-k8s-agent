// Package policy is the Slack instantiation of the shared chat authorization
// set: subjects are Slack user IDs (U…) and scopes are channel IDs (C…/G…/D…).
package policy

import (
	"errors"
	"strings"

	"github.com/aurora-capcompute/aurora-capcompute/aurora"

	"github.com/aurora-capcompute/aurora-k8s-agent/internal/binding"
	chatpolicy "github.com/aurora-capcompute/aurora-k8s-agent/internal/chat/policy"
)

// User and Set are the Slack-typed authorization records.
type (
	User = chatpolicy.User[string]
	Set  = chatpolicy.Set[string]
)

var config = chatpolicy.Config[string]{
	Source: "slack",
	Noun:   "Slack",
	ParseSubject: func(raw string) (string, error) {
		id := strings.TrimSpace(raw)
		if id == "" {
			return "", errors.New("policy contains an empty Slack user ID")
		}
		return id, nil
	},
	ParseScope: func(raw string) (string, error) {
		id := strings.TrimSpace(raw)
		if id == "" {
			return "", errors.New("policy contains an empty Slack channel ID")
		}
		return id, nil
	},
}

// Load reads and parses a Slack policy file.
func Load(path string, provider aurora.DispatcherProvider) (*Set, error) {
	return chatpolicy.Load(path, config, provider)
}

// Parse builds a Slack authorization set from file or bindings format.
func Parse(raw []byte, provider aurora.DispatcherProvider) (*Set, error) {
	return chatpolicy.Parse(raw, config, provider)
}

// FromResolved builds a Slack authorization set from resolved bindings.
func FromResolved(resolved []binding.Resolved) (*Set, error) {
	return chatpolicy.FromResolved(config, resolved)
}
