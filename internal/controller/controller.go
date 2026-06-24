package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"

	"aurora-capcompute/aurora"

	"aurora-k8s-agent/internal/apis/aurora/v1alpha1"
	"aurora-k8s-agent/internal/oci"
)

// GroupVersionResources for the control-plane kinds.
var (
	brainGVR    = schema.GroupVersionResource{Group: v1alpha1.Group, Version: v1alpha1.Version, Resource: "brains"}
	instanceGVR = schema.GroupVersionResource{Group: v1alpha1.Group, Version: v1alpha1.Version, Resource: "functioninstances"}
	channelGVR  = schema.GroupVersionResource{Group: v1alpha1.Group, Version: v1alpha1.Version, Resource: "channels"}
)

// Controller watches the Aurora control-plane resources and reconciles them into
// runtime configuration via Reconcile, writing status back to each resource and
// invoking onResolved with the resolved set. It implements source.Source so it
// runs alongside the channels.
type Controller struct {
	dyn        dynamic.Interface
	namespace  string
	resync     time.Duration
	puller     oci.Puller
	provider   aurora.DispatcherProvider
	onResolved func(Resolved)
	logger     *slog.Logger

	listers map[schema.GroupVersionResource]cache.GenericLister
	trigger chan struct{}
}

// New builds a Controller. namespace empty means all namespaces.
func New(
	dyn dynamic.Interface,
	namespace string,
	puller oci.Puller,
	provider aurora.DispatcherProvider,
	onResolved func(Resolved),
	logger *slog.Logger,
) *Controller {
	return &Controller{
		dyn:        dyn,
		namespace:  namespace,
		resync:     10 * time.Minute,
		puller:     puller,
		provider:   provider,
		onResolved: onResolved,
		logger:     logger,
		listers:    make(map[schema.GroupVersionResource]cache.GenericLister),
		trigger:    make(chan struct{}, 1),
	}
}

// Kind implements source.Source.
func (c *Controller) Kind() string { return "controller" }

// Start runs the informers and reconciles on every change until ctx is cancelled.
func (c *Controller) Start(ctx context.Context) error {
	factory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(c.dyn, c.resync, c.namespace, nil)
	handler := cache.ResourceEventHandlerFuncs{
		AddFunc:    func(any) { c.enqueue() },
		UpdateFunc: func(any, any) { c.enqueue() },
		DeleteFunc: func(any) { c.enqueue() },
	}
	for _, gvr := range []schema.GroupVersionResource{brainGVR, instanceGVR, channelGVR} {
		informer := factory.ForResource(gvr)
		if _, err := informer.Informer().AddEventHandler(handler); err != nil {
			return fmt.Errorf("add handler for %s: %w", gvr.Resource, err)
		}
		c.listers[gvr] = informer.Lister()
	}

	factory.Start(ctx.Done())
	if synced := factory.WaitForCacheSync(ctx.Done()); !allSynced(synced) {
		return fmt.Errorf("control-plane informer caches failed to sync")
	}
	c.logger.Info("controller caches synced; reconciling")
	c.reconcileOnce(ctx)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.trigger:
			c.reconcileOnce(ctx)
		}
	}
}

// enqueue requests a reconcile, coalescing bursts into a single pending run.
func (c *Controller) enqueue() {
	select {
	case c.trigger <- struct{}{}:
	default:
	}
}

func (c *Controller) reconcileOnce(ctx context.Context) {
	var in Inputs
	brainObjs := c.list(brainGVR)
	for _, u := range brainObjs {
		var spec v1alpha1.BrainSpec
		if err := decodeSpec(u, &spec); err != nil {
			c.logger.Warn("decode Brain spec", "name", u.GetName(), "error", err)
			continue
		}
		in.Brains = append(in.Brains, NamedBrain{Name: u.GetName(), Spec: spec})
	}
	channelObjs := c.list(channelGVR)
	for _, u := range channelObjs {
		var spec v1alpha1.ChannelSpec
		if err := decodeSpec(u, &spec); err != nil {
			c.logger.Warn("decode Channel spec", "name", u.GetName(), "error", err)
			continue
		}
		in.Channels = append(in.Channels, NamedChannel{Name: u.GetName(), Spec: spec})
	}
	instanceObjs := c.list(instanceGVR)
	for _, u := range instanceObjs {
		var spec v1alpha1.FunctionInstanceSpec
		if err := decodeSpec(u, &spec); err != nil {
			c.logger.Warn("decode FunctionInstance spec", "name", u.GetName(), "error", err)
			continue
		}
		in.Instances = append(in.Instances, NamedInstance{Name: u.GetName(), Spec: spec})
	}

	res := Reconcile(ctx, in, c.puller, c.provider)

	for _, u := range brainObjs {
		c.writeStatus(ctx, brainGVR, u, res.BrainStatus[u.GetName()])
	}
	for _, u := range channelObjs {
		c.writeStatus(ctx, channelGVR, u, res.ChannelStatus[u.GetName()])
	}
	for _, u := range instanceObjs {
		c.writeStatus(ctx, instanceGVR, u, res.InstanceStatus[u.GetName()])
	}

	c.logger.Info("controller reconciled",
		"brains", len(in.Brains), "channels", len(in.Channels),
		"instances", len(in.Instances), "bindings", len(res.Bindings))
	if c.onResolved != nil {
		c.onResolved(res)
	}
}

func (c *Controller) list(gvr schema.GroupVersionResource) []*unstructured.Unstructured {
	lister, ok := c.listers[gvr]
	if !ok {
		return nil
	}
	objs, err := lister.List(labels.Everything())
	if err != nil {
		c.logger.Warn("list resources", "resource", gvr.Resource, "error", err)
		return nil
	}
	out := make([]*unstructured.Unstructured, 0, len(objs))
	for _, obj := range objs {
		if u, ok := obj.(*unstructured.Unstructured); ok {
			out = append(out, u)
		}
	}
	return out
}

// writeStatus patches the resource's status subresource. status is any of the
// v1alpha1 *Status structs.
func (c *Controller) writeStatus(ctx context.Context, gvr schema.GroupVersionResource, u *unstructured.Unstructured, status any) {
	raw, err := json.Marshal(status)
	if err != nil {
		return
	}
	var statusMap map[string]any
	if err := json.Unmarshal(raw, &statusMap); err != nil {
		return
	}
	cp := u.DeepCopy()
	cp.Object["status"] = statusMap
	if _, err := c.dyn.Resource(gvr).Namespace(cp.GetNamespace()).UpdateStatus(ctx, cp, metav1.UpdateOptions{}); err != nil {
		c.logger.Warn("update status", "resource", gvr.Resource, "name", u.GetName(), "error", err)
	}
}

// decodeSpec decodes the object's spec via JSON so that json.RawMessage settings
// round-trip correctly (the unstructured converter does not handle []byte fields).
func decodeSpec(u *unstructured.Unstructured, out any) error {
	spec, ok := u.Object["spec"]
	if !ok {
		return fmt.Errorf("object %q has no spec", u.GetName())
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

func allSynced(synced map[schema.GroupVersionResource]bool) bool {
	for _, ok := range synced {
		if !ok {
			return false
		}
	}
	return len(synced) > 0
}
