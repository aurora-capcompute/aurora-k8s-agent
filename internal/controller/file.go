package controller

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"aurora-k8s-agent/internal/apis/aurora/v1alpha1"
	"aurora-k8s-agent/internal/oci"

	"aurora-capcompute/aurora"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
)

// FileSource is a control plane backed by the filesystem instead of the
// Kubernetes API. It reads Brain/Channel/FunctionInstance manifests from a
// directory and runs the same Reconcile as the in-cluster controller, re-scanning
// on an interval. It lets the agent run without a cluster (the "fs" control
// plane), pointing directly at resource files on disk.
type FileSource struct {
	dir        string
	resync     time.Duration
	puller     oci.Puller
	provider   aurora.DispatcherProvider
	onResolved func(Resolved)
	logger     *slog.Logger
}

// NewFileSource builds a filesystem control plane reading manifests from dir and
// re-scanning every resync (defaulting to 30s when non-positive).
func NewFileSource(
	dir string,
	resync time.Duration,
	puller oci.Puller,
	provider aurora.DispatcherProvider,
	onResolved func(Resolved),
	logger *slog.Logger,
) *FileSource {
	if resync <= 0 {
		resync = 30 * time.Second
	}
	return &FileSource{
		dir:        dir,
		resync:     resync,
		puller:     puller,
		provider:   provider,
		onResolved: onResolved,
		logger:     logger,
	}
}

// Kind implements source.Source.
func (f *FileSource) Kind() string { return "fs-control-plane" }

// Start reconciles the manifest directory once, then re-scans on the resync
// interval until ctx is cancelled.
func (f *FileSource) Start(ctx context.Context) error {
	f.reconcile(ctx)
	ticker := time.NewTicker(f.resync)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			f.reconcile(ctx)
		}
	}
}

func (f *FileSource) reconcile(ctx context.Context) {
	objs, err := readManifests(f.dir)
	if err != nil {
		f.logger.Warn("fs control plane: read manifests", "dir", f.dir, "error", err)
		return
	}
	in := inputsFromObjects(objs, f.logger)
	res := Reconcile(ctx, in, f.puller, f.provider)
	f.logger.Info("fs control plane reconciled",
		"dir", f.dir, "brains", len(in.Brains), "channels", len(in.Channels),
		"instances", len(in.Instances), "bindings", len(res.Bindings))
	if f.onResolved != nil {
		f.onResolved(res)
	}
}

// inputsFromObjects builds Reconcile inputs from manifest objects, dispatching on
// kind. Unknown kinds are logged and skipped.
func inputsFromObjects(objs []*unstructured.Unstructured, logger *slog.Logger) Inputs {
	var in Inputs
	for _, u := range objs {
		switch u.GetKind() {
		case "Brain":
			var spec v1alpha1.BrainSpec
			if err := decodeSpec(u, &spec); err != nil {
				logger.Warn("decode Brain spec", "name", u.GetName(), "error", err)
				continue
			}
			in.Brains = append(in.Brains, NamedBrain{Name: u.GetName(), Spec: spec})
		case "Channel":
			var spec v1alpha1.ChannelSpec
			if err := decodeSpec(u, &spec); err != nil {
				logger.Warn("decode Channel spec", "name", u.GetName(), "error", err)
				continue
			}
			in.Channels = append(in.Channels, NamedChannel{Name: u.GetName(), Spec: spec})
		case "FunctionInstance":
			var spec v1alpha1.FunctionInstanceSpec
			if err := decodeSpec(u, &spec); err != nil {
				logger.Warn("decode FunctionInstance spec", "name", u.GetName(), "error", err)
				continue
			}
			in.Instances = append(in.Instances, NamedInstance{Name: u.GetName(), Spec: spec})
		case "":
			// Empty document; skip.
		default:
			logger.Warn("fs control plane: unknown kind", "kind", u.GetKind(), "name", u.GetName())
		}
	}
	return in
}

// readManifests reads every YAML/JSON file in dir and decodes each document into
// an unstructured object. Multi-document YAML files are supported.
func readManifests(dir string) ([]*unstructured.Unstructured, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var objs []*unstructured.Unstructured
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		switch strings.ToLower(filepath.Ext(entry.Name())) {
		case ".yaml", ".yml", ".json":
		default:
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		decoder := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(raw), 4096)
		for {
			var m map[string]any
			if err := decoder.Decode(&m); err != nil {
				if errors.Is(err, io.EOF) {
					break
				}
				return nil, fmt.Errorf("%s: %w", entry.Name(), err)
			}
			if len(m) == 0 {
				continue
			}
			objs = append(objs, &unstructured.Unstructured{Object: m})
		}
	}
	return objs, nil
}
