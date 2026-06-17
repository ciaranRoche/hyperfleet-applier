// Package controller implements the desire reconciliation controllers for the applier agent.
// Three controllers process desires: apply, delete, and read.
package controller

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/openshift-hyperfleet/hyperfleet-adapter/pkg/desire"
	redisstore "github.com/openshift-hyperfleet/hyperfleet-adapter/pkg/desire/store/redis"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// ReconcilerConfig holds configuration for the reconciler.
type ReconcilerConfig struct {
	Store        *redisstore.Store
	Partition    string
	PollInterval time.Duration
	Logger       *slog.Logger
}

// Reconciler polls the desire store and reconciles desires against the local kube-apiserver.
type Reconciler struct {
	store        *redisstore.Store
	dynClient    dynamic.Interface
	partition    string
	pollInterval time.Duration
	logger       *slog.Logger
}

// NewReconciler creates a new reconciler with an in-cluster kube client.
func NewReconciler(cfg ReconcilerConfig) (*Reconciler, error) {
	// Build in-cluster kube config, fall back to default kubeconfig for local dev
	restCfg, err := rest.InClusterConfig()
	if err != nil {
		slog.Warn("in-cluster config not available, trying default kubeconfig", "error", err)
		loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
		configOverrides := &clientcmd.ConfigOverrides{}
		restCfg, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides).ClientConfig()
		if err != nil {
			return nil, fmt.Errorf("failed to create kube config: %w", err)
		}
	}

	dynClient, err := dynamic.NewForConfig(restCfg)
	if err != nil {
		return nil, err
	}

	return &Reconciler{
		store:        cfg.Store,
		dynClient:    dynClient,
		partition:    cfg.Partition,
		pollInterval: cfg.PollInterval,
		logger:       cfg.Logger,
	}, nil
}

// NewReconcilerWithClient creates a reconciler with a provided dynamic client (for testing).
func NewReconcilerWithClient(cfg ReconcilerConfig, dynClient dynamic.Interface) *Reconciler {
	return &Reconciler{
		store:        cfg.Store,
		dynClient:    dynClient,
		partition:    cfg.Partition,
		pollInterval: cfg.PollInterval,
		logger:       cfg.Logger,
	}
}

// Run starts the reconciliation loop. Blocks until the context is cancelled.
func (r *Reconciler) Run(ctx context.Context) error {
	r.logger.Info("starting reconciliation loop", "partition", r.partition, "interval", r.pollInterval)

	ticker := time.NewTicker(r.pollInterval)
	defer ticker.Stop()

	// Run once immediately
	r.reconcile(ctx)

	for {
		select {
		case <-ctx.Done():
			r.logger.Info("reconciliation loop stopped")
			return nil
		case <-ticker.C:
			r.reconcile(ctx)
		}
	}
}

// reconcile processes all desire types for this partition.
func (r *Reconciler) reconcile(ctx context.Context) {
	r.reconcileApplyDesires(ctx)
	r.reconcileDeleteDesires(ctx)
	r.reconcileReadDesires(ctx)
}

// reconcileApplyDesires processes all ApplyDesires.
func (r *Reconciler) reconcileApplyDesires(ctx context.Context) {
	desires, err := r.store.ListApplyDesires(ctx, r.partition)
	if err != nil {
		r.logger.Error("failed to list apply desires", "error", err)
		return
	}

	for _, d := range desires {
		r.processApplyDesire(ctx, d)
	}
}

// processApplyDesire applies a single desire's kubeContent via server-side apply.
func (r *Reconciler) processApplyDesire(ctx context.Context, d *desire.ApplyDesire) {
	log := r.logger.With("desire", d.ID.Name, "type", "apply")

	// Parse the manifest
	obj := &unstructured.Unstructured{}
	if err := obj.UnmarshalJSON(d.Spec.KubeContent); err != nil {
		log.Error("failed to unmarshal kube content", "error", err)
		r.writeApplyStatus(ctx, d, false, desire.ReasonPreCheckFailed, err.Error())
		return
	}

	// Build the GVR for the dynamic client
	gvr := schema.GroupVersionResource{
		Group:    d.Spec.TargetItem.Group,
		Version:  d.Spec.TargetItem.Version,
		Resource: d.Spec.TargetItem.Resource,
	}

	log.Info("applying resource",
		"gvr", gvr.String(),
		"namespace", obj.GetNamespace(),
		"name", obj.GetName(),
	)

	// Server-side apply with force
	var resource dynamic.ResourceInterface
	if obj.GetNamespace() != "" {
		resource = r.dynClient.Resource(gvr).Namespace(obj.GetNamespace())
	} else {
		resource = r.dynClient.Resource(gvr)
	}

	_, err := resource.Apply(ctx, obj.GetName(), obj, ApplyOptions())
	if err != nil {
		log.Error("SSA apply failed", "error", err)
		r.writeApplyStatus(ctx, d, false, desire.ReasonKubeAPIError, err.Error())
		return
	}

	log.Info("resource applied successfully")
	r.writeApplyStatus(ctx, d, true, desire.ReasonApplied, "resource applied via SSA")
}

// reconcileDeleteDesires processes all DeleteDesires.
func (r *Reconciler) reconcileDeleteDesires(ctx context.Context) {
	desires, err := r.store.ListDeleteDesires(ctx, r.partition)
	if err != nil {
		r.logger.Error("failed to list delete desires", "error", err)
		return
	}

	for _, d := range desires {
		r.processDeleteDesire(ctx, d)
	}
}

// processDeleteDesire deletes a resource and confirms it is gone.
func (r *Reconciler) processDeleteDesire(ctx context.Context, d *desire.DeleteDesire) {
	log := r.logger.With("desire", d.ID.Name, "type", "delete")

	gvr := d.Spec.TargetItem.GVR()

	var resource dynamic.ResourceInterface
	if d.Spec.TargetItem.Namespace != "" {
		resource = r.dynClient.Resource(gvr).Namespace(d.Spec.TargetItem.Namespace)
	} else {
		resource = r.dynClient.Resource(gvr)
	}

	// Try to get the resource first
	existing, err := resource.Get(ctx, d.Spec.TargetItem.Name, GetOptions())
	if err != nil {
		if isNotFound(err) {
			// Resource is gone, mark as successful
			log.Info("resource confirmed deleted")
			r.writeDeleteStatus(ctx, d, true, desire.ReasonDeleted, "resource not found")
			return
		}
		log.Error("failed to get resource", "error", err)
		r.writeDeleteStatus(ctx, d, false, desire.ReasonKubeAPIError, err.Error())
		return
	}

	// Resource exists, check for deletion timestamp
	if existing.GetDeletionTimestamp() != nil {
		log.Info("resource has deletion timestamp, waiting for finalizers")
		r.writeDeleteStatus(ctx, d, false, desire.ReasonWaitingForDeletion,
			"deletion timestamp set, waiting for finalizers")
		return
	}

	// Issue the delete
	log.Info("deleting resource", "name", d.Spec.TargetItem.Name)
	if err := resource.Delete(ctx, d.Spec.TargetItem.Name, DeleteOptions()); err != nil {
		if isNotFound(err) {
			log.Info("resource already gone")
			r.writeDeleteStatus(ctx, d, true, desire.ReasonDeleted, "resource not found")
			return
		}
		log.Error("delete failed", "error", err)
		r.writeDeleteStatus(ctx, d, false, desire.ReasonKubeAPIError, err.Error())
		return
	}

	// Re-check if resource is gone
	_, err = resource.Get(ctx, d.Spec.TargetItem.Name, GetOptions())
	if err != nil && isNotFound(err) {
		log.Info("resource confirmed deleted after delete call")
		r.writeDeleteStatus(ctx, d, true, desire.ReasonDeleted, "resource deleted")
		return
	}

	// Still present (finalizers), mark as waiting
	r.writeDeleteStatus(ctx, d, false, desire.ReasonWaitingForDeletion,
		"delete issued, resource still present")
}

// reconcileReadDesires processes all ReadDesires.
func (r *Reconciler) reconcileReadDesires(ctx context.Context) {
	desires, err := r.store.ListReadDesires(ctx, r.partition)
	if err != nil {
		r.logger.Error("failed to list read desires", "error", err)
		return
	}

	for _, d := range desires {
		r.processReadDesire(ctx, d)
	}
}

// processReadDesire reads a resource from the kube API and mirrors it into status.
func (r *Reconciler) processReadDesire(ctx context.Context, d *desire.ReadDesire) {
	log := r.logger.With("desire", d.ID.Name, "type", "read")

	gvr := d.Spec.TargetItem.GVR()

	var resource dynamic.ResourceInterface
	if d.Spec.TargetItem.Namespace != "" {
		resource = r.dynClient.Resource(gvr).Namespace(d.Spec.TargetItem.Namespace)
	} else {
		resource = r.dynClient.Resource(gvr)
	}

	obj, err := resource.Get(ctx, d.Spec.TargetItem.Name, GetOptions())
	if err != nil {
		if isNotFound(err) {
			log.Debug("resource not found for read desire")
			r.writeReadStatus(ctx, d, false, desire.ReasonPreCheckFailed, "resource not found", nil)
			return
		}
		log.Error("failed to get resource for read desire", "error", err)
		r.writeReadStatus(ctx, d, false, desire.ReasonKubeAPIError, err.Error(), nil)
		return
	}

	// Marshal the live object
	kubeContent, err := obj.MarshalJSON()
	if err != nil {
		log.Error("failed to marshal live object", "error", err)
		r.writeReadStatus(ctx, d, false, desire.ReasonPreCheckFailed, err.Error(), nil)
		return
	}

	log.Debug("mirrored live object", "name", obj.GetName())
	r.writeReadStatus(ctx, d, true, desire.ReasonSynced, "live object mirrored", kubeContent)
}
