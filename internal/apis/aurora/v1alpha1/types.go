// Package v1alpha1 defines the Aurora control-plane API. The model is decomposed
// into three roles:
//
//   - Brain: an OCI brain artifact that *exposes an interface* — the capabilities
//     its whole delegation tree (root + children, declared in the artifact)
//     requires.
//   - typed Channel CRDs (SlackChannel/TelegramChannel/WebChannel): a transport
//     plus its channel-native user/subject abstraction and its own credentials,
//     each credential carried as a SecretSource (a tagged union).
//   - ChannelBinding: *satisfies* a brain's interface with a single flat combined
//     grant and *wires* the brain to a channel; validation happens here.
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

	KindBrain           = "Brain"
	KindSlackChannel    = "SlackChannel"
	KindTelegramChannel = "TelegramChannel"
	KindWebChannel      = "WebChannel"
	KindChannelBinding  = "ChannelBinding"
)

// ChannelKinds lists the typed channel kinds, in a stable order.
var ChannelKinds = []string{KindSlackChannel, KindTelegramChannel, KindWebChannel}

// --- Brain ---

// BrainSpec references a brain OCI artifact. The artifact's manifest declares the
// capability interface of the whole tree (root + children).
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

// --- secrets ---

// SecretSource is a tagged union describing where a credential comes from. Only
// the field selected by Type is populated.
//
//   - "inPlaceEncrypted": Ciphertext holds base64(nonce‖AES-GCM ciphertext),
//     decrypted in place with the agent's secret key. Implemented.
//   - "secretStorage": Ref points at an external secret (k8s Secret or fs).
//     Declared for forward-compatibility; not yet resolvable.
type SecretSource struct {
	Type       string        `json:"type"`
	Ciphertext string        `json:"ciphertext,omitempty"`
	Ref        *SecretKeyRef `json:"ref,omitempty"`
}

// Secret source variants.
const (
	SecretInPlaceEncrypted = "inPlaceEncrypted"
	SecretStorage          = "secretStorage"
)

// SecretKeyRef names a key within an external secret (the secretStorage variant).
type SecretKeyRef struct {
	Name string `json:"name"`
	Key  string `json:"key"`
}

// --- typed channels ---

// SlackChannelSpec is a Slack transport: its tokens plus the Slack-native
// subjects (user U… ids and channel C… ids) allowed on it.
type SlackChannelSpec struct {
	AppToken SecretSource `json:"appToken"`
	BotToken SecretSource `json:"botToken"`
	Users    []string     `json:"users"`
	Scopes   []string     `json:"scopes"`
}

// TelegramChannelSpec is a Telegram transport: its bot token plus Telegram-native
// subjects (numeric user ids and chat ids) allowed on it.
type TelegramChannelSpec struct {
	BotToken SecretSource `json:"botToken"`
	Users    []string     `json:"users"`
	Scopes   []string     `json:"scopes"`
}

// WebChannelSpec is the HTTP-driven web channel: no transport secret. Users and
// scopes are optional (the web channel is driven over the API).
type WebChannelSpec struct {
	Users  []string `json:"users,omitempty"`
	Scopes []string `json:"scopes,omitempty"`
}

// ChannelStatus reports validation state for any channel kind.
type ChannelStatus struct {
	Ready   bool   `json:"ready"`
	Message string `json:"message,omitempty"`
}

// --- ChannelBinding ---

// Capability is a capability grant: a name plus optional scoped settings.
type Capability struct {
	Name     string          `json:"name"`
	Settings json.RawMessage `json:"settings,omitempty"`
}

// ChannelRef names a typed channel: its kind (SlackChannel/TelegramChannel/
// WebChannel) and resource name.
type ChannelRef struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// ChannelBindingSpec satisfies a brain's interface and wires it to a channel.
// Allowed is the single flat combined grant for the whole brain tree: every
// capability the tree requires must be present, scoped no wider than the brain
// declares.
//
// Secrets holds encrypted credentials for this binding. Each key becomes the
// env var name the agent sets at bridge startup (e.g. key "OPENAI_API_KEY"
// becomes os.Setenv("OPENAI_API_KEY", resolvedPlaintext)). Capability settings
// in Allowed can reference these env vars by name — e.g. api_key_env:
// OPENAI_API_KEY — so the plaintext never appears in the stored manifest.
type ChannelBindingSpec struct {
	BrainRef     string                 `json:"brainRef"`
	ChannelRef   ChannelRef             `json:"channelRef"`
	SystemPrompt string                 `json:"systemPrompt,omitempty"`
	Allowed      []Capability           `json:"allowed"`
	Secrets      map[string]SecretSource `json:"secrets,omitempty"`
}

// ChannelBindingStatus reports validation state.
type ChannelBindingStatus struct {
	Ready   bool   `json:"ready"`
	Message string `json:"message,omitempty"`
}
