package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	"aurora-k8s-agent/internal/controller"
	"aurora-k8s-agent/internal/oci"
	"aurora-k8s-agent/internal/policy"
	"aurora-k8s-agent/internal/secretbox"
	slackclient "aurora-k8s-agent/internal/slack"
	"aurora-k8s-agent/internal/slackbot"
	"aurora-k8s-agent/internal/slackpolicy"
	"aurora-k8s-agent/internal/slackstate"
	"aurora-k8s-agent/internal/source"
	"aurora-k8s-agent/internal/state"
	"aurora-k8s-agent/internal/telegram"
	"aurora-k8s-agent/internal/webapi"
	"aurora-k8s-agent/internal/webchannel"
	"aurora-stores/memory"
	aurorasqlite "aurora-stores/sqlite"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

var version = "dev"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "seal-secret" {
		if err := sealSecret(); err != nil {
			fmt.Fprintln(os.Stderr, "seal-secret:", err)
			os.Exit(1)
		}
		return
	}
	if err := run(); err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("aurora-k8s-agent stopped", "error", err)
		os.Exit(1)
	}
}

// sealSecret reads a plaintext credential from stdin and prints the base64
// nonce‖AES-GCM ciphertext for an inPlaceEncrypted channel secret, using the same
// key (AURORA_SECRET_KEY) the agent decrypts with. Usage:
//
//	printf %s "$TOKEN" | aurora-k8s-agent seal-secret
func sealSecret() error {
	keyValue, err := requiredSecret("AURORA_SECRET_KEY", "AURORA_SECRET_KEY_FILE")
	if err != nil {
		return err
	}
	plain, err := io.ReadAll(os.Stdin)
	if err != nil {
		return err
	}
	plain = bytes.TrimRight(plain, "\r\n")
	out, err := secretbox.SealBase64(secretbox.DeriveKey(keyValue), plain)
	if err != nil {
		return err
	}
	fmt.Println(out)
	return nil
}

func run() error {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: parseLogLevel(env("AURORA_LOG_LEVEL", "info")),
	}))
	slog.SetDefault(logger)

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
		registry.TimerRegistration{},
	)
	mcpServers, err := mcpServersFromEnv()
	if err != nil {
		return err
	}
	if len(mcpServers) > 0 {
		provider.SetServices(registry.Services{MCPServers: mcpServers})
	}
	policyPath := env("AURORA_POLICY_PATH", "/etc/aurora/policy.json")

	dataDir := env("AURORA_DATA_DIR", "/data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}
	runtimeStore, err := aurorasqlite.Open(filepath.Join(dataDir, "aurora.db"))
	if err != nil {
		return fmt.Errorf("open Aurora store: %w", err)
	}
	defer runtimeStore.Close()

	sessionStore := memory.NewSessionStore[string, aurora.RunContext]()
	brains, err := buildBrainProvider(ctx, logger)
	if err != nil {
		return err
	}
	runtime, err := aurora.NewRuntime(ctx, aurora.Config{
		Brains:       brains,
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

	// The web channel registry is populated by control-plane reconciliation and
	// read by the API server, so create it before both.
	webChannel := webchannel.New()

	var ready atomic.Bool
	health := startHealthServer(ctx, env("AURORA_HEALTH_ADDR", ":8080"), &ready, logger)
	defer health.Shutdown(context.Background())
	if api := startAPIServer(ctx, env("AURORA_API_ADDR", ":8081"), runtime, webChannel, logger); api != nil {
		defer api.Shutdown(context.Background())
	}

	kinds := sourceKinds()
	var (
		sources []source.Source
		closers []func()
	)
	defer func() {
		for i := len(closers) - 1; i >= 0; i-- {
			closers[i]()
		}
	}()
	for _, kind := range kinds {
		var (
			src    source.Source
			closer func()
		)
		switch kind {
		case "telegram":
			src, closer, err = buildTelegram(ctx, runtime, provider, policyPath, dataDir, stateKey, logger)
		case "slack":
			src, closer, err = buildSlack(ctx, runtime, provider, policyPath, dataDir, stateKey, logger)
		default:
			return fmt.Errorf("unknown source %q (want telegram or slack)", kind)
		}
		if err != nil {
			return err
		}
		sources = append(sources, src)
		if closer != nil {
			closers = append(closers, closer)
		}
	}

	switch controlPlaneKind() {
	case "k8s":
		ctrl, err := buildController(provider, webChannel, logger)
		if err != nil {
			return err
		}
		sources = append(sources, ctrl)
	case "fs":
		fsControl, err := buildFileControlPlane(provider, webChannel, logger)
		if err != nil {
			return err
		}
		sources = append(sources, fsControl)
	case "none":
		// No control plane.
	default:
		return fmt.Errorf("unknown control plane %q (want k8s, fs, or none)", controlPlaneKind())
	}

	ready.Store(true)
	if len(sources) == 0 {
		// Headless: no chat channels and no control plane. Serve the HTTP API (if
		// enabled) and block until shutdown.
		logger.Info("Aurora agent started (headless)", "version", version)
		<-ctx.Done()
		return nil
	}
	logger.Info("Aurora agent started", "version", version, "sources", kinds, "control_plane", controlPlaneKind())
	return source.Run(ctx, logger, sources...)
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

// buildFileControlPlane builds the filesystem control plane reading manifests
// from AURORA_RESOURCES_DIR (re-scanned every AURORA_RESOURCES_RESYNC, default 30s).
func buildFileControlPlane(provider aurora.DispatcherProvider, webChannel *webchannel.Channel, logger *slog.Logger) (source.Source, error) {
	dir := strings.TrimSpace(os.Getenv("AURORA_RESOURCES_DIR"))
	if dir == "" {
		return nil, errors.New("fs control plane requires AURORA_RESOURCES_DIR")
	}
	resync := 30 * time.Second
	if raw := strings.TrimSpace(os.Getenv("AURORA_RESOURCES_RESYNC")); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid AURORA_RESOURCES_RESYNC %q: %w", raw, err)
		}
		resync = parsed
	}
	onResolved := func(res controller.Resolved) {
		logger.Info("control plane resolved",
			"brainRefs", res.BrainRefs, "bindings", len(res.Bindings))
		webChannel.Apply(res)
	}
	return controller.NewFileSource(dir, resync,
		oci.NewRemotePuller(ociOptionsFromEnv()...), provider, onResolved, logger), nil
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

// buildBrainProvider selects the brain source. With AURORA_BRAINS set (a comma
// list of OCI references) brains are pulled from registries; otherwise the
// embedded kubernetes-agent brain is used. Registry auth comes from
// AURORA_REGISTRY_USERNAME/PASSWORD; AURORA_REGISTRY_PLAIN_HTTP=true uses HTTP.
func buildBrainProvider(ctx context.Context, logger *slog.Logger) (aurora.BrainProvider, error) {
	refs := splitList(os.Getenv("AURORA_BRAINS"))
	if len(refs) == 0 {
		return assembly.BrainProvider{}, nil
	}
	provider, err := assembly.NewOCIBrainProvider(ctx, refs, os.Getenv("AURORA_BRAIN_DEFAULT"), oci.NewRemotePuller(ociOptionsFromEnv()...))
	if err != nil {
		return nil, fmt.Errorf("load brains from OCI: %w", err)
	}
	logger.Info("loaded brains from OCI", "count", len(refs), "default", provider.DefaultID())
	return provider, nil
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

// buildController constructs the in-cluster control-plane controller. It watches
// Brain, the typed channels (SlackChannel/TelegramChannel/WebChannel), and
// ChannelBinding resources and reconciles them, writing status back. Enabled with
// AURORA_CONTROLLER=true; AURORA_CONTROLLER_NAMESPACE scopes
// the watch (empty = all namespaces).
func buildController(provider aurora.DispatcherProvider, webChannel *webchannel.Channel, logger *slog.Logger) (source.Source, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("controller requires in-cluster config: %w", err)
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("controller dynamic client: %w", err)
	}
	onResolved := func(res controller.Resolved) {
		logger.Info("control plane resolved",
			"brainRefs", res.BrainRefs, "bindings", len(res.Bindings))
		webChannel.Apply(res)
	}
	return controller.New(dyn, os.Getenv("AURORA_CONTROLLER_NAMESPACE"),
		oci.NewRemotePuller(ociOptionsFromEnv()...), provider, onResolved, logger), nil
}

// splitList parses a comma-separated env value, trimming and dropping blanks.
func splitList(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func buildTelegram(
	ctx context.Context,
	runtime aurora.Runtime,
	provider aurora.DispatcherProvider,
	policyPath, dataDir string,
	stateKey []byte,
	logger *slog.Logger,
) (source.Source, func(), error) {
	token, err := requiredSecret("TELEGRAM_BOT_TOKEN", "TELEGRAM_BOT_TOKEN_FILE")
	if err != nil {
		return nil, nil, err
	}
	policies, err := policy.Load(policyPath, provider)
	if err != nil {
		return nil, nil, err
	}
	bridgeStore, err := state.Open(filepath.Join(dataDir, "telegram.db"), stateKey)
	if err != nil {
		return nil, nil, fmt.Errorf("open Telegram state: %w", err)
	}
	client := telegram.NewClient(token)
	if baseURL := os.Getenv("TELEGRAM_API_BASE_URL"); baseURL != "" {
		client.SetBaseURL(baseURL)
	}
	identity, err := client.GetMe(ctx)
	if err != nil {
		bridgeStore.Close()
		return nil, nil, fmt.Errorf("validate Telegram bot token: %w", err)
	}
	service := bot.New(runtime, client, bridgeStore, policies, identity, logger)
	return service, func() { bridgeStore.Close() }, nil
}

func buildSlack(
	_ context.Context,
	runtime aurora.Runtime,
	provider aurora.DispatcherProvider,
	policyPath, dataDir string,
	stateKey []byte,
	logger *slog.Logger,
) (source.Source, func(), error) {
	appToken, err := requiredSecret("SLACK_APP_TOKEN", "SLACK_APP_TOKEN_FILE")
	if err != nil {
		return nil, nil, err
	}
	botToken, err := requiredSecret("SLACK_BOT_TOKEN", "SLACK_BOT_TOKEN_FILE")
	if err != nil {
		return nil, nil, err
	}
	policies, err := slackpolicy.Load(policyPath, provider)
	if err != nil {
		return nil, nil, err
	}
	bridgeStore, err := slackstate.Open(filepath.Join(dataDir, "slack.db"), stateKey)
	if err != nil {
		return nil, nil, fmt.Errorf("open Slack state: %w", err)
	}
	client, err := slackclient.NewClient(appToken, botToken)
	if err != nil {
		bridgeStore.Close()
		return nil, nil, err
	}
	service := slackbot.New(runtime, client, bridgeStore, policies, logger)
	return service, func() { bridgeStore.Close() }, nil
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
	return startServer(ctx, "health", address, mux, logger)
}

// startAPIServer serves the agent HTTP API (read-only execution-graph
// projections, interactive control, and the live event stream) on its own port,
// separate from the health/probe port so it can be exposed via a Service.
// Disabled when address is empty.
func startAPIServer(
	ctx context.Context,
	address string,
	runtime aurora.Runtime,
	channel *webchannel.Channel,
	logger *slog.Logger,
) *http.Server {
	if strings.TrimSpace(address) == "" {
		return nil
	}
	return startServer(ctx, "api", address, webapi.Handler(runtime, channel), logger)
}

func startServer(ctx context.Context, name, address string, handler http.Handler, logger *slog.Logger) *http.Server {
	server := &http.Server{Addr: address, Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error(name+" server", "error", err)
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
