// Package v1alpha1 defines the Aurora control-plane API. One logical agent is
// described by a single resource:
//
//   - Manifest: inlines the brain OCI artifact, an embedded array of typed-ADT
//     channels (Slack/Telegram/Web transports, each carrying its own credentials
//     and channel-native subjects), the capability tree (capabilities + delegation
//     children), and the system prompt. One Manifest fully describes one agent.
//
// A channel is a typed ADT: the Kind field selects which payload (Slack/Telegram/
// Web) is populated. The per-transport payload structs and the SecretSource/
// SettingValue tagged unions are shared building blocks.
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

	// KindManifest is the single control-plane kind.
	KindManifest = "Manifest"

	// Channel kind discriminators, used within a Manifest's channels array.
	KindSlackChannel    = "SlackChannel"
	KindTelegramChannel = "TelegramChannel"
	KindWebChannel      = "WebChannel"
)

// ChannelKinds lists the typed channel kinds, in a stable order.
var ChannelKinds = []string{KindSlackChannel, KindTelegramChannel, KindWebChannel}

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

// --- channel payloads ---

// SlackChannelSpec is a Slack transport payload: its tokens plus the Slack-native
// subjects (user U… ids and channel C… ids) allowed on it.
type SlackChannelSpec struct {
	AppToken SecretSource `json:"appToken"`
	BotToken SecretSource `json:"botToken"`
	Users    []string     `json:"users"`
	Scopes   []string     `json:"scopes"`
}

// TelegramChannelSpec is a Telegram transport payload: its bot token plus
// Telegram-native subjects (numeric user ids and chat ids) allowed on it.
type TelegramChannelSpec struct {
	BotToken SecretSource `json:"botToken"`
	Users    []string     `json:"users"`
	Scopes   []string     `json:"scopes"`
}

// WebChannelUser is one login credential carried sealed in the channel payload.
// The name is in plaintext; the password is a SecretSource (typically
// inPlaceEncrypted). POST /api/login validates the pair and returns the channel
// bearer token on success.
type WebChannelUser struct {
	Name     string       `json:"name"`
	Password SecretSource `json:"password"`
}

// WebChannelSpec is the HTTP-driven web channel payload. Token is the bearer
// credential that gates all web API requests. Users are login credentials:
// clients that don't know the token can exchange a username/password via POST
// /api/login to receive it.
type WebChannelSpec struct {
	Token  *SecretSource    `json:"token,omitempty"`
	Users  []WebChannelUser `json:"users,omitempty"`
	Scopes []string         `json:"scopes,omitempty"`
}

// Channel is one element of a Manifest's channels array: a typed ADT discriminated
// by Kind, with the matching payload (Slack/Telegram/Web) populated. Name is the
// channel's identifier within the Manifest.
type Channel struct {
	Kind     string               `json:"kind"`
	Name     string               `json:"name"`
	Slack    *SlackChannelSpec    `json:"slack,omitempty"`
	Telegram *TelegramChannelSpec `json:"telegram,omitempty"`
	Web      *WebChannelSpec      `json:"web,omitempty"`
}

// --- tools ---

// AgentToolType is the tool `type` for a sub-agent node in the composition tree.
const AgentToolType = "core.agent"

// Tool is one node in a manifest's unified composition tree: leaf I/O tools and
// sub-agents share one shape. Type selects the dispatcher; Name is the local
// handle the brain routes to. For a `core.agent` tool, Settings carries the
// sub-agent's `code` (short WASM name), `system_prompt`, and `on_failure`, and
// Tools holds the sub-agent's own composition. Hidden keeps a tool dispatchable
// but off the brain's discoverable menu (e.g. the LLM cognition tool).
type Tool struct {
	Name     string                  `json:"name"`
	Type     string                  `json:"type"`
	Settings map[string]SettingValue `json:"settings,omitempty"`
	Tools    []Tool                  `json:"tools,omitempty"`
	Hidden   bool                    `json:"hidden,omitempty"`
}

// --- Manifest ---

// Brain references the OCI brain artifact, inlined into the Manifest. The artifact
// bundles one or more named WASM binaries (the root brain plus any child brains).
type Brain struct {
	// Artifact is the OCI reference (e.g. ghcr.io/org/brain-k8s:1.4).
	Artifact string `json:"artifact"`
	// PullSecretRef names a Secret (docker-config or basic auth) for the registry.
	PullSecretRef string `json:"pullSecretRef,omitempty"`
}

// ManifestSpec is the single control-plane resource: an inlined brain, the
// channels it serves, the root system prompt, and the unified tool composition
// tree (leaf tools plus `core.agent` sub-agents).
type ManifestSpec struct {
	Brain        Brain        `json:"brain"`
	Channels     []Channel    `json:"channels"`
	SystemPrompt SettingValue `json:"systemPrompt,omitempty"`
	Tools        []Tool       `json:"tools"`
}

// ManifestStatus reports resolution state for a Manifest.
type ManifestStatus struct {
	Ready   bool   `json:"ready"`
	Message string `json:"message,omitempty"`
	Digest  string `json:"digest,omitempty"`  // root manifest content digest
	BrainID string `json:"brainID,omitempty"` // resolved root brain id (digest/name)
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
