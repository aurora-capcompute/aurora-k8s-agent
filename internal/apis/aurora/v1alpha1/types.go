// Package v1alpha1 defines the Aurora control-plane API: Brain (an OCI brain
// artifact), FunctionInstance (a brain bound to a granted capability subset and a
// channel — the "manifest" as a deployable instance), and Channel (a transport +
// its credentials). The controller watches these and configures the agent.
//
// These are plain spec/status structs decoded from unstructured objects via the
// dynamic client; they are not registered runtime.Objects, so no deepcopy/scheme
// codegen is required.
package v1alpha1

import "encoding/json"

const (
	// Group is the API group for Aurora control-plane resources.
	Group = "aurora.dev"
	// Version is the API version.
	Version = "v1alpha1"

	KindBrain            = "Brain"
	KindFunctionInstance = "FunctionInstance"
	KindChannel          = "Channel"
)

// BrainSpec references a brain OCI artifact.
type BrainSpec struct {
	// Artifact is the OCI reference (e.g. ghcr.io/org/brain-k8s:1.4).
	Artifact string `json:"artifact"`
	// PullSecretRef names a Secret (docker-config or basic auth) for the registry.
	PullSecretRef string `json:"pullSecretRef,omitempty"`
}

// BrainStatus reports the resolved brain.
type BrainStatus struct {
	Digest       string   `json:"digest,omitempty"`
	BrainID      string   `json:"brainID,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
	Ready        bool     `json:"ready"`
	Message      string   `json:"message,omitempty"`
}

// Capability is a capability grant: a name plus optional scoped settings.
type Capability struct {
	Name     string          `json:"name"`
	Settings json.RawMessage `json:"settings,omitempty"`
}

// Subjects identifies who may drive this instance on its channel.
type Subjects struct {
	// Users are source-specific subject IDs (Telegram numeric IDs, Slack U…).
	Users []string `json:"users"`
	// Scopes are source-specific scope IDs (Telegram chat IDs, Slack channel IDs).
	Scopes []string `json:"scopes"`
}

// Child is a delegation child: another brain reachable via call.<name>.
type Child struct {
	Name         string       `json:"name"`
	BrainRef     string       `json:"brainRef"`
	SystemPrompt string       `json:"systemPrompt,omitempty"`
	Capabilities []Capability `json:"capabilities,omitempty"`
}

// FunctionInstanceSpec binds a brain to a granted capability subset and a channel.
type FunctionInstanceSpec struct {
	BrainRef     string       `json:"brainRef"`
	SystemPrompt string       `json:"systemPrompt,omitempty"`
	Capabilities []Capability `json:"capabilities"`
	ChannelRef   string       `json:"channelRef"`
	Subjects     Subjects     `json:"subjects"`
	Children     []Child      `json:"children,omitempty"`
}

// FunctionInstanceStatus reports validation state.
type FunctionInstanceStatus struct {
	Ready   bool   `json:"ready"`
	Message string `json:"message,omitempty"`
}

// ChannelSpec is a transport and its credentials.
type ChannelSpec struct {
	// Source is the transport kind: "telegram" or "slack".
	Source string `json:"source"`
	// SecretRef names the Secret holding the transport tokens.
	SecretRef string `json:"secretRef"`
}

// ChannelStatus reports validation state.
type ChannelStatus struct {
	Ready   bool   `json:"ready"`
	Message string `json:"message,omitempty"`
}
