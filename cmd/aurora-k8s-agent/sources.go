package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"

	"github.com/aurora-capcompute/aurora-capcompute/aurora"

	"github.com/aurora-capcompute/aurora-k8s-agent/internal/assembly"
	"github.com/aurora-capcompute/aurora-k8s-agent/internal/controller"
	"github.com/aurora-capcompute/aurora-k8s-agent/internal/oci"
	"github.com/aurora-capcompute/aurora-k8s-agent/internal/source"

	tgchat "github.com/aurora-capcompute/aurora-k8s-agent/internal/chat/telegram"
	tgpolicy "github.com/aurora-capcompute/aurora-k8s-agent/internal/chat/telegram/policy"
	tgstate "github.com/aurora-capcompute/aurora-k8s-agent/internal/chat/telegram/state"
	tgapi "github.com/aurora-capcompute/aurora-k8s-agent/internal/transport/telegram"

	slchat "github.com/aurora-capcompute/aurora-k8s-agent/internal/chat/slack"
	slpolicy "github.com/aurora-capcompute/aurora-k8s-agent/internal/chat/slack/policy"
	slstate "github.com/aurora-capcompute/aurora-k8s-agent/internal/chat/slack/state"
	slapi "github.com/aurora-capcompute/aurora-k8s-agent/internal/transport/slack"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

// buildFileControlPlane builds the filesystem control plane reading manifests
// from cfg.ResourcesDir (re-scanned every cfg.ResourcesResync).
func buildFileControlPlane(cfg Config, provider aurora.DispatcherProvider, onResolved func(controller.Resolved), logger *slog.Logger) (source.Source, error) {
	if cfg.ResourcesDir == "" {
		return nil, errors.New("fs control plane requires AURORA_RESOURCES_DIR")
	}
	return controller.NewFileSource(cfg.ResourcesDir, cfg.ResourcesResync,
		oci.NewRemotePuller(cfg.OCIOptions...), provider, onResolved, logger), nil
}

// buildBrainProvider selects the brain source loaded at startup. With cfg.BrainRefs
// set (registry refs or "oci-layout:<dir>:<tag>" for a registry-less on-disk
// layout) those brains are loaded up front. With BrainRefs empty but the fs
// control plane active, Brain CRDs in cfg.ResourcesDir are pre-scanned so the
// runtime can restore previous sessions before the async reconcile loop fires.
// Otherwise the agent boots with EmptyProvider and receives brains at runtime via
// Brain CRDs hot-loaded through runtime.SetBrains.
func buildBrainProvider(ctx context.Context, cfg Config, logger *slog.Logger) (aurora.BrainProvider, error) {
	refs := cfg.BrainRefs
	if len(refs) == 0 && cfg.ControlPlane == "fs" && cfg.ResourcesDir != "" {
		refs = controller.ScanBrainArtifacts(cfg.ResourcesDir)
	}
	if len(refs) == 0 {
		return assembly.EmptyProvider{}, nil
	}
	provider, err := assembly.NewOCIBrainProvider(ctx, refs, cfg.BrainDefault, oci.NewRemotePuller(cfg.OCIOptions...))
	if err != nil {
		return nil, fmt.Errorf("load brains from OCI: %w", err)
	}
	logger.Info("loaded brains from OCI", "count", len(refs), "default", provider.DefaultID())
	return provider, nil
}

// buildController constructs the in-cluster control-plane controller. It watches
// Brain, the typed channels (SlackChannel/TelegramChannel/WebChannel), and
// ChannelBinding resources and reconciles them, writing status back.
// cfg.ControllerNamespace scopes the watch (empty = all namespaces).
func buildController(cfg Config, provider aurora.DispatcherProvider, onResolved func(controller.Resolved), logger *slog.Logger) (source.Source, error) {
	restCfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("controller requires in-cluster config: %w", err)
	}
	dyn, err := dynamic.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("controller dynamic client: %w", err)
	}
	return controller.New(dyn, cfg.ControllerNamespace,
		oci.NewRemotePuller(cfg.OCIOptions...), provider, onResolved, logger), nil
}

func buildTelegram(
	ctx context.Context,
	cfg Config,
	runtime aurora.Runtime,
	provider aurora.DispatcherProvider,
	stateKey []byte,
	logger *slog.Logger,
) (source.Source, func(), error) {
	token, err := requiredSecret("TELEGRAM_BOT_TOKEN", "TELEGRAM_BOT_TOKEN_FILE")
	if err != nil {
		return nil, nil, err
	}
	policies, err := tgpolicy.Load(cfg.PolicyPath, provider)
	if err != nil {
		return nil, nil, err
	}
	bridgeStore, err := tgstate.Open(filepath.Join(cfg.DataDir, "telegram.db"), stateKey)
	if err != nil {
		return nil, nil, fmt.Errorf("open Telegram state: %w", err)
	}
	client := tgapi.NewClient(token)
	if cfg.TelegramBaseURL != "" {
		client.SetBaseURL(cfg.TelegramBaseURL)
	}
	identity, err := client.GetMe(ctx)
	if err != nil {
		bridgeStore.Close()
		return nil, nil, fmt.Errorf("validate Telegram bot token: %w", err)
	}
	service := tgchat.New(runtime, client, bridgeStore, policies, identity, logger)
	return service, func() { bridgeStore.Close() }, nil
}

func buildSlack(
	_ context.Context,
	cfg Config,
	runtime aurora.Runtime,
	provider aurora.DispatcherProvider,
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
	policies, err := slpolicy.Load(cfg.PolicyPath, provider)
	if err != nil {
		return nil, nil, err
	}
	bridgeStore, err := slstate.Open(filepath.Join(cfg.DataDir, "slack.db"), stateKey)
	if err != nil {
		return nil, nil, fmt.Errorf("open Slack state: %w", err)
	}
	client, err := slapi.NewClient(appToken, botToken)
	if err != nil {
		bridgeStore.Close()
		return nil, nil, err
	}
	service := slchat.New(runtime, client, bridgeStore, policies, logger)
	return service, func() { bridgeStore.Close() }, nil
}
