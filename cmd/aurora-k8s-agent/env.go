package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/aurora-capcompute/aurora-dispatchers/mcp"

	"github.com/aurora-capcompute/aurora-k8s-agent/internal/oci"
)

// requiredSecret reads a secret from a *_FILE path (preferred) or its value env
// var, returning an error when neither is set.
func requiredSecret(valueEnv, fileEnv string) (string, error) {
	if path := strings.TrimSpace(os.Getenv(fileEnv)); path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read %s: %w", fileEnv, err)
		}
		if value := strings.TrimSpace(string(raw)); value != "" {
			return value, nil
		}
	}
	if value := strings.TrimSpace(os.Getenv(valueEnv)); value != "" {
		return value, nil
	}
	return "", fmt.Errorf("%s or %s is required", valueEnv, fileEnv)
}

// encryptionKey derives a 32-byte key from a secret: a base64-encoded 32-byte key
// is used directly, otherwise the secret is hashed.
func encryptionKey(value string) ([]byte, error) {
	if decoded, err := base64.StdEncoding.DecodeString(value); err == nil && len(decoded) == 32 {
		return decoded, nil
	}
	sum := sha256.Sum256([]byte(value))
	return sum[:], nil
}

// env returns the trimmed value of name or fallback when unset/blank.
func env(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

// ociOptionsFromEnv builds registry-auth options shared by the brain provider and
// the controller's puller.
func ociOptionsFromEnv() []oci.Option {
	var opts []oci.Option
	if user := os.Getenv("AURORA_REGISTRY_USERNAME"); user != "" {
		opts = append(opts, oci.WithBasicAuth(user, os.Getenv("AURORA_REGISTRY_PASSWORD")))
	}
	if strings.EqualFold(os.Getenv("AURORA_REGISTRY_PLAIN_HTTP"), "true") {
		opts = append(opts, oci.WithPlainHTTP(true))
	}
	return opts
}

func mcpServersFromEnv() (map[string]mcp.ServerConfig, error) {
	raw := strings.TrimSpace(os.Getenv("AURORA_MCP_SERVERS"))
	if raw == "" {
		return nil, nil
	}
	var servers map[string]mcp.ServerConfig
	if err := json.Unmarshal([]byte(raw), &servers); err != nil {
		return nil, fmt.Errorf("decode AURORA_MCP_SERVERS: %w", err)
	}
	for id, server := range servers {
		if strings.TrimSpace(server.ID) == "" {
			server.ID = id
		}
		servers[id] = server
	}
	return servers, nil
}

func parseLogLevel(value string) slog.Level {
	var level slog.Level
	if err := level.UnmarshalText([]byte(value)); err != nil {
		return slog.LevelInfo
	}
	return level
}
