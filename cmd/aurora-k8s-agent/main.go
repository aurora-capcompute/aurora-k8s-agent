package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/aurora-capcompute/aurora-capcompute/aurora"
	"github.com/aurora-capcompute/aurora-dispatchers-helm/helm"
	"github.com/aurora-capcompute/aurora-dispatchers-k8s/k8s"
	"github.com/aurora-capcompute/aurora-dispatchers-llm/openaillm"
	"github.com/aurora-capcompute/aurora-dispatchers/registry"
	"github.com/aurora-capcompute/aurora-k8s-agent/internal/assembly"
	"github.com/aurora-capcompute/aurora-k8s-agent/internal/channelsup"
	"github.com/aurora-capcompute/aurora-k8s-agent/internal/controller"
	"github.com/aurora-capcompute/aurora-k8s-agent/internal/secrets"
	"github.com/aurora-capcompute/aurora-k8s-agent/internal/source"
	"github.com/aurora-capcompute/aurora-k8s-agent/internal/webchannel"
	"github.com/aurora-capcompute/aurora-stores/memory"
	aurorasqlite "github.com/aurora-capcompute/aurora-stores/sqlite"
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
	if len(os.Args) > 1 && os.Args[1] == "pack-brain" {
		if err := packBrain(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "pack-brain:", err)
			os.Exit(1)
		}
		return
	}
	if err := run(); err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("aurora-k8s-agent stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
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
	if len(cfg.MCPServers) > 0 {
		provider.SetServices(registry.Services{MCPServers: cfg.MCPServers})
	}

	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}
	runtimeStore, err := aurorasqlite.Open(filepath.Join(cfg.DataDir, "aurora.db"))
	if err != nil {
		return fmt.Errorf("open Aurora store: %w", err)
	}
	defer runtimeStore.Close()

	sessionStore := memory.NewSessionStore[string, aurora.RunContext]()
	brains, err := buildBrainProvider(ctx, cfg, logger)
	if err != nil {
		return err
	}
	runtime, err := aurora.NewRuntime(ctx, aurora.Config{
		Brains:       brains,
		Dispatchers:  provider,
		Log:          runtimeStore,
		Leases:       runtimeStore,
		SessionStore: sessionStore,
		TaskSecret:   []byte(taskSecret),
		TenantID:     cfg.TenantID,
		InstanceID:   cfg.InstanceID,
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
	health := startHealthServer(ctx, cfg.HealthAddr, &ready, logger)
	defer health.Shutdown(context.Background())
	if api := startAPIServer(ctx, cfg.APIAddr, runtime, webChannel, logger); api != nil {
		defer api.Shutdown(context.Background())
	}

	var (
		sources []source.Source
		closers []func()
	)
	defer func() {
		for i := len(closers) - 1; i >= 0; i-- {
			closers[i]()
		}
	}()

	// When the control plane is enabled it owns the chat channels: a supervisor
	// builds one live bridge per typed channel CRD, tokens resolved from the
	// channel's SecretSource. onResolved fans each reconciliation out to the web
	// channel registry and the supervisor.
	var supervisor *channelsup.Supervisor
	onResolved := func(res controller.Resolved) {
		logger.Info("control plane resolved",
			"brains", len(res.Brains), "bindings", len(res.Bindings), "channels", len(res.Channels))
		// Hot-load the brains declared by Brain CRDs into the running runtime, so
		// the agent boots with none and gains them as resources appear.
		brainSources := make([]aurora.BrainSource, len(res.Brains))
		for i, b := range res.Brains {
			brainSources[i] = aurora.BrainSource{ID: b.ID, Wasm: b.Wasm}
		}
		if err := runtime.SetBrains(ctx, brainSources); err != nil {
			logger.Error("apply brains from control plane", "error", err)
		}
		webChannel.Apply(res)
		if supervisor != nil {
			supervisor.Apply(res)
		}
	}

	if cfg.controlPlaneEnabled() {
		if secretKey, keyErr := requiredSecret("AURORA_SECRET_KEY", "AURORA_SECRET_KEY_FILE"); keyErr != nil {
			logger.Warn("AURORA_SECRET_KEY not set; channel CRDs will not start live bridges", "error", keyErr)
		} else {
			supervisor = channelsup.New(runtime, secrets.NewInPlace(secretKey), cfg.DataDir, stateKey,
				cfg.TelegramBaseURL, logger)
			sources = append(sources, supervisor)
		}
	} else {
		// No control plane: the legacy env/file path builds a single client per
		// configured source.
		for _, kind := range cfg.Sources {
			var (
				src    source.Source
				closer func()
			)
			switch kind {
			case "telegram":
				src, closer, err = buildTelegram(ctx, cfg, runtime, provider, stateKey, logger)
			case "slack":
				src, closer, err = buildSlack(ctx, cfg, runtime, provider, stateKey, logger)
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
	}

	switch cfg.ControlPlane {
	case "k8s":
		ctrl, err := buildController(cfg, provider, onResolved, logger)
		if err != nil {
			return err
		}
		sources = append(sources, ctrl)
	case "fs":
		fsControl, err := buildFileControlPlane(cfg, provider, onResolved, logger)
		if err != nil {
			return err
		}
		sources = append(sources, fsControl)
	case "none":
		// No control plane.
	default:
		return fmt.Errorf("unknown control plane %q (want k8s, fs, or none)", cfg.ControlPlane)
	}

	ready.Store(true)
	if len(sources) == 0 {
		// Headless: no chat channels and no control plane. Serve the HTTP API (if
		// enabled) and block until shutdown.
		logger.Info("Aurora agent started (headless)", "version", version)
		<-ctx.Done()
		return nil
	}
	logger.Info("Aurora agent started", "version", version,
		"sources", cfg.Sources, "control_plane", cfg.ControlPlane, "channel_supervisor", supervisor != nil)
	return source.Run(ctx, logger, sources...)
}
