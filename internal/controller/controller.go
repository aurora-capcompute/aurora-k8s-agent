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

	"github.com/aurora-capcompute/aurora-capcompute/aurora"

	"github.com/aurora-capcompute/aurora-k8s-agent/internal/apis/aurora/v1alpha1"
	"github.com/aurora-capcompute/aurora-k8s-agent/internal/oci"
)

// GroupVersionResources for the control-plane kinds.
var (
	brainGVR    = gvr("brains")
	slackGVR    = gvr("slackchannels")
	telegramGVR = gvr("telegramchannels")
	webGVR      = gvr("webchannels")
	bindingGVR  = gvr("channelbindings")

	// controlPlaneGVRs is the watch set, in a stable order.
	controlPlaneGVRs = []schema.GroupVersionResource{brainGVR, slackGVR, telegramGVR, webGVR, bindingGVR}
)

func gvr(resource string) schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: v1alpha1.Group, Version: v1alpha1.Version, Resource: resource}
}

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
	for _, res := range controlPlaneGVRs {
		informer := factory.ForResource(res)
		if _, err := informer.Informer().AddEventHandler(handler); err != nil {
			return fmt.Errorf("add handler for %s: %w", res.Resource, err)
		}
		c.listers[res] = informer.Lister()
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
	brainObjs := c.list(brainGVR)
	slackObjs := c.list(slackGVR)
	telegramObjs := c.list(telegramGVR)
	webObjs := c.list(webGVR)
	bindingObjs := c.list(bindingGVR)

	var in Inputs
	for _, u := range brainObjs {
		var spec v1alpha1.BrainSpec
		if c.decode(u, v1alpha1.KindBrain, &spec) {
			in.Brains = append(in.Brains, NamedBrain{Name: u.GetName(), Spec: spec})
		}
	}
	for _, u := range slackObjs {
		var spec v1alpha1.SlackChannelSpec
		if c.decode(u, v1alpha1.KindSlackChannel, &spec) {
			in.SlackChannels = append(in.SlackChannels, NamedSlackChannel{Name: u.GetName(), Spec: spec})
		}
	}
	for _, u := range telegramObjs {
		var spec v1alpha1.TelegramChannelSpec
		if c.decode(u, v1alpha1.KindTelegramChannel, &spec) {
			in.TelegramChannels = append(in.TelegramChannels, NamedTelegramChannel{Name: u.GetName(), Spec: spec})
		}
	}
	for _, u := range webObjs {
		var spec v1alpha1.WebChannelSpec
		if c.decode(u, v1alpha1.KindWebChannel, &spec) {
			in.WebChannels = append(in.WebChannels, NamedWebChannel{Name: u.GetName(), Spec: spec})
		}
	}
	for _, u := range bindingObjs {
		var spec v1alpha1.ChannelBindingSpec
		if c.decode(u, v1alpha1.KindChannelBinding, &spec) {
			in.Bindings = append(in.Bindings, NamedBinding{Name: u.GetName(), Spec: spec})
		}
	}

	res := Reconcile(ctx, in, c.puller, c.provider)

	for _, u := range brainObjs {
		c.writeStatus(ctx, brainGVR, u, res.BrainStatus[u.GetName()])
	}
	for _, u := range slackObjs {
		c.writeStatus(ctx, slackGVR, u, res.ChannelStatus[ChannelKey(v1alpha1.KindSlackChannel, u.GetName())])
	}
	for _, u := range telegramObjs {
		c.writeStatus(ctx, telegramGVR, u, res.ChannelStatus[ChannelKey(v1alpha1.KindTelegramChannel, u.GetName())])
	}
	for _, u := range webObjs {
		c.writeStatus(ctx, webGVR, u, res.ChannelStatus[ChannelKey(v1alpha1.KindWebChannel, u.GetName())])
	}
	for _, u := range bindingObjs {
		c.writeStatus(ctx, bindingGVR, u, res.BindingStatus[u.GetName()])
	}

	c.logger.Info("controller reconciled",
		"brains", len(in.Brains),
		"channels", len(in.SlackChannels)+len(in.TelegramChannels)+len(in.WebChannels),
		"bindings", len(res.Bindings))
	if c.onResolved != nil {
		c.onResolved(res)
	}
}

// decode decodes an object's spec, logging and skipping on error.
func (c *Controller) decode(u *unstructured.Unstructured, kind string, out any) bool {
	if err := decodeSpec(u, out); err != nil {
		c.logger.Warn("decode "+kind+" spec", "name", u.GetName(), "error", err)
		return false
	}
	return true
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
