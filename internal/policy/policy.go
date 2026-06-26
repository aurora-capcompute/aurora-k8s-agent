// Package policy is the Telegram instantiation of the shared chat authorization
// set: subjects and scopes are numeric Telegram IDs.
package policy

import (
	"fmt"
	"strconv"
	"strings"

	"aurora-capcompute/aurora"

	"aurora-k8s-agent/internal/binding"
	chatpolicy "aurora-k8s-agent/internal/chat/policy"
)

// User and Set are the Telegram-typed authorization records.
type (
	User = chatpolicy.User[int64]
	Set  = chatpolicy.Set[int64]
)

var config = chatpolicy.Config[int64]{
	Source: "telegram",
	Noun:   "Telegram",
	ParseSubject: func(raw string) (int64, error) {
		id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil || id <= 0 {
			return 0, fmt.Errorf("invalid Telegram user ID %q", raw)
		}
		return id, nil
	},
	ParseScope: func(raw string) (int64, error) {
		// Telegram chat IDs may be negative (supergroups); only zero is invalid.
		id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil || id == 0 {
			return 0, fmt.Errorf("invalid Telegram chat ID %q", raw)
		}
		return id, nil
	},
}

// Load reads and parses a Telegram policy file.
func Load(path string, provider aurora.DispatcherProvider) (*Set, error) {
	return chatpolicy.Load(path, config, provider)
}

// Parse builds a Telegram authorization set from file or bindings format.
func Parse(raw []byte, provider aurora.DispatcherProvider) (*Set, error) {
	return chatpolicy.Parse(raw, config, provider)
}

// FromResolved builds a Telegram authorization set from resolved bindings.
func FromResolved(resolved []binding.Resolved) (*Set, error) {
	return chatpolicy.FromResolved(config, resolved)
}
