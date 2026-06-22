package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"aurora-capcompute/aurora"
	"aurora-dispatchers-helm/helm"
	"aurora-dispatchers-k8s/k8s"
	"aurora-dispatchers-llm/openaillm"
	"aurora-dispatchers/mcp"
	"aurora-dispatchers/registry"
	"aurora-k8s-agent/internal/assembly"
	"aurora-k8s-agent/internal/bot"
	"aurora-k8s-agent/internal/policy"
	"aurora-k8s-agent/internal/state"
	"aurora-k8s-agent/internal/telegram"
	"aurora-stores/memory"
	aurorasqlite "aurora-stores/sqlite"
)

var version = "dev"

func main() {
	if err := run(); err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("aurora-k8s-agent stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: parseLogLevel(env("AURORA_LOG_LEVEL", "info")),
	}))
	slog.SetDefault(logger)

	token, err := requiredSecret("TELEGRAM_BOT_TOKEN", "TELEGRAM_BOT_TOKEN_FILE")
	if err != nil {
		return err
	}
	taskSecret, err := requiredSecret("AURORA_TASK_SECRET", "AURORA_TASK_SECRET_FILE")
	if err != nil {
		return err
	}
	stateSecret, err := requiredSecret("AURORA_STATE_KEY", "AURORA_STATE_KEY_FILE")
	if err != nil {
		return err
	}
	stateKey, err := encryptionKey(stateSecret)
	if err != nil {
		return err
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	provider := assembly.NewProvider(
		openaillm.Registration{},
		k8s.Registration{},
		helm.Registration{},
		registry.InternetRegistration{},
		registry.MCPRegistration{},
		registry.AuroraLogRegistration{},
	)
	mcpServers, err := mcpServersFromEnv()
	if err != nil {
		return err
	}
	if len(mcpServers) > 0 {
		provider.SetServices(registry.Services{MCPServers: mcpServers})
	}
	policies, err := policy.Load(env("AURORA_POLICY_PATH", "/etc/aurora/policy.json"), provider)
	if err != nil {
		return err
	}

	dataDir := env("AURORA_DATA_DIR", "/data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}
	runtimeStore, err := aurorasqlite.Open(filepath.Join(dataDir, "aurora.db"))
	if err != nil {
		return fmt.Errorf("open Aurora store: %w", err)
	}
	defer runtimeStore.Close()
	bridgeStore, err := state.Open(filepath.Join(dataDir, "telegram.db"), stateKey)
	if err != nil {
		return fmt.Errorf("open Telegram state: %w", err)
	}
	defer bridgeStore.Close()

	sessionStore := memory.NewSessionStore[string, aurora.RunContext]()
	runtime, err := aurora.NewRuntime(ctx, aurora.Config{
		Brains:       assembly.BrainProvider{},
		Dispatchers:  provider,
		StateStore:   runtimeStore,
		TaskStore:    runtimeStore,
		SessionStore: sessionStore,
		TaskSecret:   []byte(taskSecret),
		TenantID:     env("AURORA_TENANT_ID", aurora.DefaultTenantID),
		InstanceID:   env("AURORA_INSTANCE_ID", ""),
	})
	if err != nil {
		return fmt.Errorf("create Aurora runtime: %w", err)
	}
	defer func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer closeCancel()
		if err := runtime.Close(closeCtx); err != nil {
			logger.Error("close runtime", "error", err)
		}
	}()

	client := telegram.NewClient(token)
	if baseURL := os.Getenv("TELEGRAM_API_BASE_URL"); baseURL != "" {
		client.SetBaseURL(baseURL)
	}
	identity, err := client.GetMe(ctx)
	if err != nil {
		return fmt.Errorf("validate Telegram bot token: %w", err)
	}

	var ready atomic.Bool
	health := startHealthServer(ctx, env("AURORA_HEALTH_ADDR", ":8080"), &ready, logger)
	defer health.Shutdown(context.Background())

	service := bot.New(runtime, client, bridgeStore, policies, identity, logger)
	if err := service.Recover(ctx); err != nil {
		return fmt.Errorf("recover Telegram sessions: %w", err)
	}
	ready.Store(true)
	logger.Info("Aurora Kubernetes agent started",
		"version", version, "telegram_bot", identity.Username, "telegram_bot_id", identity.ID)
	return service.Run(ctx)
}

func startHealthServer(
	ctx context.Context,
	address string,
	ready *atomic.Bool,
	logger *slog.Logger,
) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/livez", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if !ready.Load() {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready\n"))
	})
	server := &http.Server{Addr: address, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("health server", "error", err)
		}
	}()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	return server
}

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

func encryptionKey(value string) ([]byte, error) {
	if decoded, err := base64.StdEncoding.DecodeString(value); err == nil && len(decoded) == 32 {
		return decoded, nil
	}
	sum := sha256.Sum256([]byte(value))
	return sum[:], nil
}

func env(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
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
