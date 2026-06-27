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

	"github.com/fsnotify/fsnotify"

	"github.com/aurora-capcompute/aurora-k8s-agent/internal/apis/aurora/v1alpha1"
	"github.com/aurora-capcompute/aurora-k8s-agent/internal/oci"

	"github.com/aurora-capcompute/aurora-capcompute/aurora"
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

// Start reconciles the manifest directory once, then re-reconciles on filesystem
// changes (via an fsnotify watch, debounced) and on the resync interval as a
// fallback, until ctx is cancelled. If the watcher cannot be created the source
// degrades to polling only.
func (f *FileSource) Start(ctx context.Context) error {
	f.reconcile(ctx)
	ticker := time.NewTicker(f.resync)
	defer ticker.Stop()

	var (
		fsEvents <-chan fsnotify.Event
		fsErrors <-chan error
	)
	if watcher, err := fsnotify.NewWatcher(); err != nil {
		f.logger.Warn("fs control plane: watcher unavailable, polling only", "error", err)
	} else {
		defer watcher.Close()
		if err := watcher.Add(f.dir); err != nil {
			f.logger.Warn("fs control plane: watch dir failed, polling only", "dir", f.dir, "error", err)
		} else {
			fsEvents, fsErrors = watcher.Events, watcher.Errors
		}
	}

	// Coalesce a burst of file events (an editor writing several files) into one
	// reconcile a short moment later.
	debounce := time.NewTimer(0)
	if !debounce.Stop() {
		<-debounce.C
	}
	defer debounce.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			f.reconcile(ctx)
		case _, ok := <-fsEvents:
			if !ok {
				fsEvents = nil
				continue
			}
			debounce.Reset(200 * time.Millisecond)
		case err, ok := <-fsErrors:
			if !ok {
				fsErrors = nil
				continue
			}
			f.logger.Warn("fs control plane: watch error", "error", err)
		case <-debounce.C:
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
		"dir", f.dir, "brains", len(in.Brains),
		"channels", len(in.SlackChannels)+len(in.TelegramChannels)+len(in.WebChannels),
		"bindings", len(res.Bindings))
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
		case v1alpha1.KindBrain:
			var spec v1alpha1.BrainSpec
			if decodeFsSpec(u, v1alpha1.KindBrain, &spec, logger) {
				in.Brains = append(in.Brains, NamedBrain{Name: u.GetName(), Spec: spec})
			}
		case v1alpha1.KindSlackChannel:
			var spec v1alpha1.SlackChannelSpec
			if decodeFsSpec(u, v1alpha1.KindSlackChannel, &spec, logger) {
				in.SlackChannels = append(in.SlackChannels, NamedSlackChannel{Name: u.GetName(), Spec: spec})
			}
		case v1alpha1.KindTelegramChannel:
			var spec v1alpha1.TelegramChannelSpec
			if decodeFsSpec(u, v1alpha1.KindTelegramChannel, &spec, logger) {
				in.TelegramChannels = append(in.TelegramChannels, NamedTelegramChannel{Name: u.GetName(), Spec: spec})
			}
		case v1alpha1.KindWebChannel:
			var spec v1alpha1.WebChannelSpec
			if decodeFsSpec(u, v1alpha1.KindWebChannel, &spec, logger) {
				in.WebChannels = append(in.WebChannels, NamedWebChannel{Name: u.GetName(), Spec: spec})
			}
		case v1alpha1.KindChannelBinding:
			var spec v1alpha1.ChannelBindingSpec
			if decodeFsSpec(u, v1alpha1.KindChannelBinding, &spec, logger) {
				in.Bindings = append(in.Bindings, NamedBinding{Name: u.GetName(), Spec: spec})
			}
		case "":
			// Empty document; skip.
		default:
			logger.Warn("fs control plane: unknown kind", "kind", u.GetKind(), "name", u.GetName())
		}
	}
	return in
}

func decodeFsSpec(u *unstructured.Unstructured, kind string, out any, logger *slog.Logger) bool {
	if err := decodeSpec(u, out); err != nil {
		logger.Warn("decode "+kind+" spec", "name", u.GetName(), "error", err)
		return false
	}
	return true
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
