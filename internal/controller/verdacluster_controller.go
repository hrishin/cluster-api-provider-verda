/*
Copyright 2026 The Verda CAPI Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/cluster-api/util/conditions"
	"sigs.k8s.io/cluster-api/util/finalizers"
	"sigs.k8s.io/cluster-api/util/patch"
	"sigs.k8s.io/cluster-api/util/paused"
	"sigs.k8s.io/cluster-api/util/predicates"

	infrav1 "github.com/hrishin/verda-capi/api/v1beta1"
)

// VerdaClusterReconciler reconciles a VerdaCluster object.
//
// Verda has no managed network or load balancer, so cluster-level
// infrastructure is limited to validating that a control plane endpoint has
// been provided and surfacing it to Cluster API.
type VerdaClusterReconciler struct {
	client.Client
	// WatchFilterValue is the label value used to filter events prior to reconciliation.
	WatchFilterValue string
}

// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=verdaclusters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=verdaclusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=verdaclusters/finalizers,verbs=update
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters;clusters/status,verbs=get;list;watch

// Reconcile brings a VerdaCluster in line with its Cluster owner.
func (r *VerdaClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (_ ctrl.Result, reterr error) {
	log := ctrl.LoggerFrom(ctx)

	verdaCluster := &infrav1.VerdaCluster{}
	if err := r.Get(ctx, req.NamespacedName, verdaCluster); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Add the finalizer first so that no infrastructure can be left behind if
	// the object is deleted between now and the first patch.
	if finalizerAdded, err := finalizers.EnsureFinalizer(ctx, r.Client, verdaCluster, infrav1.ClusterFinalizer); err != nil || finalizerAdded {
		return ctrl.Result{}, err
	}

	cluster, err := util.GetOwnerCluster(ctx, r.Client, verdaCluster.ObjectMeta)
	if err != nil {
		return ctrl.Result{}, err
	}
	if cluster == nil {
		log.Info("Waiting for Cluster controller to set OwnerRef on VerdaCluster")
		return ctrl.Result{}, nil
	}
	ctx = ctrl.LoggerInto(ctx, log.WithValues("Cluster", client.ObjectKeyFromObject(cluster)))

	if isPaused, requeue, err := paused.EnsurePausedCondition(ctx, r.Client, cluster, verdaCluster); err != nil || isPaused || requeue {
		return ctrl.Result{}, err
	}

	patchHelper, err := patch.NewHelper(verdaCluster, r.Client)
	if err != nil {
		return ctrl.Result{}, err
	}
	defer func() {
		if err := patchVerdaCluster(ctx, patchHelper, verdaCluster); err != nil {
			reterr = kerrorsJoin(reterr, err)
		}
	}()

	if !verdaCluster.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, verdaCluster)
	}
	return r.reconcileNormal(ctx, cluster, verdaCluster)
}

func (r *VerdaClusterReconciler) reconcileNormal(ctx context.Context, cluster *clusterv1.Cluster, verdaCluster *infrav1.VerdaCluster) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)

	// The endpoint may be set on the VerdaCluster directly or on the Cluster
	// (for example by a control plane provider). Either satisfies the contract.
	endpoint := verdaCluster.Spec.ControlPlaneEndpoint
	if endpoint.Host == "" {
		endpoint = cluster.Spec.ControlPlaneEndpoint
	}
	if endpoint.Host == "" {
		log.Info("Waiting for a control plane endpoint: Verda has no managed load balancer, set spec.controlPlaneEndpoint on the VerdaCluster to a DNS name or IP that will route to the control plane machines")
		conditions.Set(verdaCluster, metav1.Condition{
			Type:    infrav1.ControlPlaneEndpointReadyCondition,
			Status:  metav1.ConditionFalse,
			Reason:  "EndpointNotSet",
			Message: "spec.controlPlaneEndpoint must be set; Verda does not provide a managed load balancer",
		})
		return ctrl.Result{}, nil
	}
	if endpoint.Port == 0 {
		endpoint.Port = 6443
	}
	verdaCluster.Spec.ControlPlaneEndpoint = endpoint

	conditions.Set(verdaCluster, metav1.Condition{
		Type:   infrav1.ControlPlaneEndpointReadyCondition,
		Status: metav1.ConditionTrue,
		Reason: clusterv1.ReadyReason,
	})
	verdaCluster.Status.Initialization.Provisioned = ptr.To(true)
	return ctrl.Result{}, nil
}

func (r *VerdaClusterReconciler) reconcileDelete(_ context.Context, verdaCluster *infrav1.VerdaCluster) (ctrl.Result, error) {
	// Nothing is provisioned at the cluster level; machines clean up their own
	// instances. Cluster API waits for all Machines to be gone before deleting
	// the InfraCluster.
	controllerutil.RemoveFinalizer(verdaCluster, infrav1.ClusterFinalizer)
	return ctrl.Result{}, nil
}

// patchVerdaCluster summarises the Ready condition and persists spec and status.
func patchVerdaCluster(ctx context.Context, patchHelper *patch.Helper, verdaCluster *infrav1.VerdaCluster) error {
	ready := metav1.Condition{Type: clusterv1.ReadyCondition, Status: metav1.ConditionTrue, Reason: clusterv1.ReadyReason}
	switch {
	case !verdaCluster.DeletionTimestamp.IsZero():
		ready.Status = metav1.ConditionFalse
		ready.Reason = clusterv1.DeletingReason
	case !conditions.IsTrue(verdaCluster, infrav1.ControlPlaneEndpointReadyCondition):
		ready.Status = metav1.ConditionFalse
		ready.Reason = clusterv1.NotReadyReason
		ready.Message = conditions.GetMessage(verdaCluster, infrav1.ControlPlaneEndpointReadyCondition)
	}
	conditions.Set(verdaCluster, ready)

	return patchHelper.Patch(ctx, verdaCluster, patch.WithOwnedConditions{Conditions: []string{
		clusterv1.PausedCondition,
		clusterv1.ReadyCondition,
		infrav1.ControlPlaneEndpointReadyCondition,
	}})
}

// SetupWithManager sets up the controller with the Manager.
func (r *VerdaClusterReconciler) SetupWithManager(ctx context.Context, mgr ctrl.Manager, options controller.Options) error {
	predicateLog := ctrl.LoggerFrom(ctx).WithValues("controller", "verdacluster")
	return ctrl.NewControllerManagedBy(mgr).
		For(&infrav1.VerdaCluster{}).
		WithOptions(options).
		WithEventFilter(predicates.ResourceHasFilterLabel(mgr.GetScheme(), predicateLog, r.WatchFilterValue)).
		WithEventFilter(predicates.ResourceIsNotExternallyManaged(mgr.GetScheme(), predicateLog)).
		Watches(
			&clusterv1.Cluster{},
			handler.EnqueueRequestsFromMapFunc(util.ClusterToInfrastructureMapFunc(ctx, infrav1.GroupVersion.WithKind("VerdaCluster"), mgr.GetClient(), &infrav1.VerdaCluster{})),
			builder.WithPredicates(predicates.ClusterPausedTransitions(mgr.GetScheme(), predicateLog)),
		).
		Named("verdacluster").
		Complete(r)
}

// kerrorsJoin joins two errors, tolerating nils.
func kerrorsJoin(a, b error) error {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	default:
		return fmt.Errorf("%w; %w", a, b)
	}
}

var _ predicate.Predicate = predicate.Funcs{}
