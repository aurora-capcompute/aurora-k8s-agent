package main

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/aurora-capcompute/aurora-capcompute/aurora"
	"github.com/aurora-capcompute/aurora-dispatchers/mcp"

	"github.com/aurora-capcompute/aurora-k8s-agent/internal/oci"
)

// Config is the agent's runtime configuration, parsed once from the environment
// at startup so the rest of the program reads typed fields instead of scattered
// os.Getenv calls. Secrets are deliberately excluded: they are read lazily (and
// only when needed) via requiredSecret so a missing token fails with a precise
// message at the point of use.
type Config struct {
	LogLevel        slog.Level
	PolicyPath      string
	DataDir         string
	TenantID        string
	InstanceID      string
	HealthAddr      string
	APIAddr         string
	TelegramBaseURL string

	// ControlPlane is "k8s", "fs", or "none".
	ControlPlane        string
	ControllerNamespace string
	ResourcesDir        string
	ResourcesResync     time.Duration

	// Sources lists the enabled chat sources for the no-control-plane path.
	Sources []string

	// BrainRefs are the brains loaded at startup; empty boots brain-less.
	BrainRefs    []string
	BrainDefault string

	MCPServers map[string]mcp.ServerConfig
	OCIOptions []oci.Option
}

// loadConfig parses the environment into a Config, failing fast on any malformed
// value (durations, MCP server JSON).
func loadConfig() (Config, error) {
	cfg := Config{
		LogLevel:            parseLogLevel(env("AURORA_LOG_LEVEL", "info")),
		PolicyPath:          env("AURORA_POLICY_PATH", "/etc/aurora/policy.json"),
		DataDir:             env("AURORA_DATA_DIR", "/data"),
		TenantID:            env("AURORA_TENANT_ID", aurora.DefaultTenantID),
		InstanceID:          env("AURORA_INSTANCE_ID", ""),
		HealthAddr:          env("AURORA_HEALTH_ADDR", ":8080"),
		APIAddr:             env("AURORA_API_ADDR", ":8081"),
		TelegramBaseURL:     strings.TrimSpace(os.Getenv("TELEGRAM_API_BASE_URL")),
		ControlPlane:        controlPlaneKind(),
		ControllerNamespace: os.Getenv("AURORA_CONTROLLER_NAMESPACE"),
		ResourcesDir:        strings.TrimSpace(os.Getenv("AURORA_RESOURCES_DIR")),
		ResourcesResync:     30 * time.Second,
		Sources:             sourceKinds(),
		BrainRefs:           splitList(os.Getenv("AURORA_BRAINS")),
		BrainDefault:        strings.TrimSpace(os.Getenv("AURORA_BRAIN_DEFAULT")),
		OCIOptions:          ociOptionsFromEnv(),
	}
	if raw := strings.TrimSpace(os.Getenv("AURORA_RESOURCES_RESYNC")); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			return Config{}, fmt.Errorf("invalid AURORA_RESOURCES_RESYNC %q: %w", raw, err)
		}
		cfg.ResourcesResync = parsed
	}
	servers, err := mcpServersFromEnv()
	if err != nil {
		return Config{}, err
	}
	cfg.MCPServers = servers
	return cfg, nil
}

// Enabled reports whether a control plane (k8s or fs) owns the channels.
func (c Config) controlPlaneEnabled() bool {
	return c.ControlPlane == "k8s" || c.ControlPlane == "fs"
}

// controlPlaneKind selects the control-plane channel: "k8s" (the in-cluster
// informer), "fs" (read resource manifests from AURORA_RESOURCES_DIR), or "none".
// AURORA_CONTROL_PLANE takes precedence; AURORA_CONTROLLER=true maps to "k8s" for
// backward compatibility; the default is "none".
func controlPlaneKind() string {
	if v := strings.TrimSpace(os.Getenv("AURORA_CONTROL_PLANE")); v != "" {
		return strings.ToLower(v)
	}
	if strings.EqualFold(os.Getenv("AURORA_CONTROLLER"), "true") {
		return "k8s"
	}
	return "none"
}

// sourceKinds resolves the enabled chat sources from AURORA_SOURCES (a
// comma-separated list, e.g. "telegram,slack"), falling back to the
// single-channel AURORA_CHANNEL and then "telegram". "none" (or an empty list)
// disables chat sources so the agent can run headless. Order is preserved;
// duplicates are dropped.
func sourceKinds() []string {
	raw := env("AURORA_SOURCES", env("AURORA_CHANNEL", "telegram"))
	seen := make(map[string]struct{})
	var kinds []string
	for _, part := range strings.Split(raw, ",") {
		kind := strings.ToLower(strings.TrimSpace(part))
		if kind == "" || kind == "none" {
			continue
		}
		if _, dup := seen[kind]; dup {
			continue
		}
		seen[kind] = struct{}{}
		kinds = append(kinds, kind)
	}
	return kinds
}
