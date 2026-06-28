package main

import (
	"errors"
	"fmt"
	"log/slog"

	"github.com/aurora-capcompute/aurora-capcompute/aurora"

	"github.com/aurora-capcompute/aurora-k8s-agent/internal/controller"
	"github.com/aurora-capcompute/aurora-k8s-agent/internal/oci"
	"github.com/aurora-capcompute/aurora-k8s-agent/internal/source"

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
