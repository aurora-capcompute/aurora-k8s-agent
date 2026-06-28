// Package v1alpha1 defines the Aurora control-plane API. The model is decomposed
// into three roles:
//
//   - Brain: an OCI brain artifact that bundles one or more named WASM binaries.
//     It carries no capability declarations — those live in the ChannelBinding.
//   - typed Channel CRDs (SlackChannel/TelegramChannel/WebChannel): a transport
//     plus its channel-native user/subject abstraction and its own credentials,
//     each credential carried as a SecretSource (a tagged union).
//   - ChannelBinding: declares the capability tree (capabilities + delegation
//     children), wires the brain to one or more channels, and resolves secrets.
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

// SettingValue is a tagged union for capability constructor arguments. Every
// capability setting — api_key, base_url, allowed_models, namespaces, etc. —
// is expressed as a SettingValue so secrets and plain config share one encoding.
//
//   - "literal":          Value holds the raw JSON for the setting.
//   - "inPlaceEncrypted": Ciphertext holds base64(nonce‖AES-GCM ciphertext).
//   - "secretStorage":    Ref points at an external secret.
type SettingValue struct {
	Type       string          `json:"type"`
	Value      json.RawMessage `json:"value,omitempty"`      // literal
	Ciphertext string          `json:"ciphertext,omitempty"` // inPlaceEncrypted
	Ref        *SecretKeyRef   `json:"ref,omitempty"`        // secretStorage
}

const SettingLiteral = "literal"

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

// WebChannelUser is one login credential stored sealed in the channel CRD.
// The name is in plaintext; the password is a SecretSource (typically
// inPlaceEncrypted). POST /api/login validates the pair and returns the channel
// bearer token on success.
type WebChannelUser struct {
	Name     string      `json:"name"`
	Password SecretSource `json:"password"`
}

// WebChannelSpec is the HTTP-driven web channel. Token is the bearer credential
// that gates all web API requests. Users are login credentials: clients that
// don't know the token can exchange a username/password via POST /api/login to
// receive it.
type WebChannelSpec struct {
	Token  *SecretSource    `json:"token,omitempty"`
	Users  []WebChannelUser `json:"users,omitempty"`
	Scopes []string         `json:"scopes,omitempty"`
}

// ChannelStatus reports validation state for any channel kind.
type ChannelStatus struct {
	Ready   bool   `json:"ready"`
	Message string `json:"message,omitempty"`
}

// --- ChannelBinding ---

// Capability is the unified declaration and grant for one capability: name,
// optional flag, and scoped ADT settings. Optional capabilities whose provider
// Normalize returns an error are silently skipped; required ones fail the binding.
type Capability struct {
	Name     string                  `json:"name"`
	Optional bool                    `json:"optional,omitempty"`
	Settings map[string]SettingValue `json:"settings,omitempty"`
}

// ChildSpec defines one node in the brain delegation tree.
// Brain names the short WASM name within the artifact (not the k8s resource name).
type ChildSpec struct {
	Name         string       `json:"name"`
	Brain        string       `json:"brain"`
	SystemPrompt string       `json:"systemPrompt,omitempty"`
	Capabilities []Capability `json:"capabilities,omitempty"`
	Children     []ChildSpec  `json:"children,omitempty"`
	OnFailure    string       `json:"onFailure,omitempty"`
}

// ChannelRef names a typed channel: its kind (SlackChannel/TelegramChannel/
// WebChannel) and resource name.
type ChannelRef struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// ChannelBindingSpec declares the capability tree and wires the brain to one or
// more channels. Capabilities is the combined declaration+grant for the root brain.
// Children defines the delegation tree. Channels replaces the old single channelRef.
type ChannelBindingSpec struct {
	BrainRef     string       `json:"brainRef"`
	Channels     []ChannelRef `json:"channels"`
	SystemPrompt SettingValue `json:"systemPrompt,omitempty"`
	Capabilities []Capability `json:"capabilities"`
	Children     []ChildSpec  `json:"children,omitempty"`
}

// ChannelBindingStatus reports validation state.
type ChannelBindingStatus struct {
	Ready   bool   `json:"ready"`
	Message string `json:"message,omitempty"`
}

// ResolveLiterals extracts only the literal settings from a settings map,
// returning them as a flat JSON object keyed by setting name.
// Non-literal settings (encrypted, ref) are omitted — callers that need
// decrypted values must resolve them before calling this function.
func ResolveLiterals(settings map[string]SettingValue) json.RawMessage {
	if len(settings) == 0 {
		return json.RawMessage(`{}`)
	}
	out := make(map[string]json.RawMessage, len(settings))
	for k, v := range settings {
		if v.Type == SettingLiteral && len(v.Value) > 0 {
			out[k] = v.Value
		}
	}
	b, err := json.Marshal(out)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}
