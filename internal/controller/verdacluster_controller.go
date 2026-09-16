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
	"errors"
	"fmt"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
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
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/cluster-api/util/conditions"
	"sigs.k8s.io/cluster-api/util/finalizers"
	"sigs.k8s.io/cluster-api/util/patch"
	"sigs.k8s.io/cluster-api/util/paused"
	"sigs.k8s.io/cluster-api/util/predicates"

	infrav1 "github.com/hrishin/verda-capi/api/v1beta1"
	"github.com/hrishin/verda-capi/internal/cloud"
	"github.com/hrishin/verda-capi/internal/loadbalancer"
)

// verdaClusterKind is the kind name used in references and owner references.
const verdaClusterKind = "VerdaCluster"

const (
	// lbPollInterval is how often a provisioning load balancer is re-checked.
	lbPollInterval = 15 * time.Second
	// lbResyncInterval bounds how long a backend drift can go unnoticed.
	lbResyncInterval = 5 * time.Minute

	// Secret keys for the load balancer SSH material.
	secretKeyClientPrivate = "ssh-privatekey"
	secretKeyClientPublic  = "ssh-publickey"
	secretKeyHostPrivate   = "host-privatekey"
	secretKeyHostPublic    = "host-publickey"
)

// VerdaClusterReconciler reconciles a VerdaCluster object.
//
// Verda has no managed network or load balancer. Cluster-level infrastructure
// is therefore either nothing (the user supplies a control plane endpoint) or
// a provider-managed haproxy instance that fronts the control plane machines.
type VerdaClusterReconciler struct {
	client.Client
	// CloudFactory yields the Verda client for a cluster's credentials.
	CloudFactory cloud.Factory
	// LoadBalancer pushes backend updates to the haproxy instance.
	LoadBalancer loadbalancer.Updater
	// WatchFilterValue is the label value used to filter events prior to reconciliation.
	WatchFilterValue string
}

// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=verdaclusters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=verdaclusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=verdaclusters/finalizers,verbs=update
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=verdamachines,verbs=get;list;watch
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters;clusters/status;machines,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete

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

	verdaClient, err := r.CloudFactory.ClientFor(ctx, cloud.Identity{Namespace: verdaCluster.Namespace, SecretName: verdaCluster.IdentitySecretName()})
	if err != nil {
		conditions.Set(verdaCluster, metav1.Condition{
			Type: infrav1.ControlPlaneEndpointReadyCondition, Status: metav1.ConditionFalse, Reason: "CredentialsUnavailable", Message: err.Error(),
		})
		return ctrl.Result{}, err
	}
	scope := &clusterScope{VerdaClusterReconciler: r, cloud: verdaClient}

	if !verdaCluster.DeletionTimestamp.IsZero() {
		return scope.reconcileDelete(ctx, verdaCluster)
	}
	return scope.reconcileNormal(ctx, cluster, verdaCluster)
}

// clusterScope is a reconciler bound to the cloud client of one cluster.
type clusterScope struct {
	*VerdaClusterReconciler
	cloud cloud.Client
}

func (r *clusterScope) reconcileNormal(ctx context.Context, cluster *clusterv1.Cluster, verdaCluster *infrav1.VerdaCluster) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)

	if verdaCluster.LoadBalancerEnabled() {
		return r.reconcileLoadBalancer(ctx, cluster, verdaCluster)
	}
	conditions.Delete(verdaCluster, infrav1.LoadBalancerReadyCondition)

	// The endpoint may be set on the VerdaCluster directly or on the Cluster
	// (for example by a control plane provider). Either satisfies the contract.
	endpoint := verdaCluster.Spec.ControlPlaneEndpoint
	if endpoint.Host == "" {
		endpoint = cluster.Spec.ControlPlaneEndpoint
	}
	if endpoint.Host == "" {
		log.Info("Waiting for a control plane endpoint: set spec.controlPlaneEndpoint to a DNS name or IP that routes to the control plane machines, or enable spec.controlPlaneLoadBalancer")
		conditions.Set(verdaCluster, metav1.Condition{
			Type:    infrav1.ControlPlaneEndpointReadyCondition,
			Status:  metav1.ConditionFalse,
			Reason:  "EndpointNotSet",
			Message: "spec.controlPlaneEndpoint must be set or spec.controlPlaneLoadBalancer.enabled must be true",
		})
		return ctrl.Result{}, nil
	}
	if endpoint.Port == 0 {
		endpoint.Port = loadbalancer.Port
	}
	verdaCluster.Spec.ControlPlaneEndpoint = endpoint
	setEndpointReady(verdaCluster)
	setFailureDomains(verdaCluster)
	verdaCluster.Status.Initialization.Provisioned = ptr.To(true)
	return ctrl.Result{}, nil
}

// setFailureDomains publishes the cluster's location as its single failure
// domain so Machines carry it in status.failureDomain.
func setFailureDomains(verdaCluster *infrav1.VerdaCluster) {
	verdaCluster.Status.FailureDomains = []clusterv1.FailureDomain{{
		Name:         verdaCluster.Spec.Location,
		ControlPlane: ptr.To(true),
	}}
}

// reconcileLoadBalancer ensures the haproxy instance exists, publishes its
// address as the control plane endpoint and keeps its backends in sync.
func (r *clusterScope) reconcileLoadBalancer(ctx context.Context, cluster *clusterv1.Cluster, verdaCluster *infrav1.VerdaCluster) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)

	keys, err := r.ensureLoadBalancerKeys(ctx, cluster, verdaCluster)
	if err != nil {
		return ctrl.Result{}, err
	}

	instance, err := r.findLoadBalancer(ctx, verdaCluster)
	if err != nil {
		return ctrl.Result{}, err
	}
	if instance == nil {
		instance, err = r.createLoadBalancer(ctx, cluster, verdaCluster, keys)
		if err != nil {
			setLBNotReady(verdaCluster, "InstanceCreateFailed", err.Error())
			return ctrl.Result{}, err
		}
		log.Info("Created load balancer instance", "instanceID", instance.ID)
	}
	verdaCluster.Status.LoadBalancer.InstanceID = instance.ID
	if instance.StartupScriptID != "" {
		verdaCluster.Status.LoadBalancer.StartupScriptID = instance.StartupScriptID
	}

	switch {
	case instance.Status == cloud.StatusRunning && instance.IP != "":
		// Ready to serve; fall through.
	case instanceGone(instance) || instance.Status == cloud.StatusError || instance.Status == cloud.StatusNoCapacity:
		setLBNotReady(verdaCluster, "InstanceFailed", fmt.Sprintf("load balancer instance %s is in state %q", instance.ID, instance.Status))
		log.Info("Load balancer instance is in a terminal state", "instanceID", instance.ID, "state", instance.Status)
		return ctrl.Result{}, nil
	default:
		setLBNotReady(verdaCluster, "InstanceProvisioning", fmt.Sprintf("load balancer instance %s is in state %q", instance.ID, instance.Status))
		return ctrl.Result{RequeueAfter: lbPollInterval}, nil
	}

	verdaCluster.Status.LoadBalancer.Address = instance.IP
	verdaCluster.Spec.ControlPlaneEndpoint = clusterv1.APIEndpoint{Host: instance.IP, Port: loadbalancer.Port}
	setEndpointReady(verdaCluster)
	setFailureDomains(verdaCluster)

	backends, err := r.controlPlaneAddresses(ctx, cluster)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !slices.Equal(backends, verdaCluster.Status.LoadBalancer.Backends) {
		log.Info("Updating load balancer backends", "address", instance.IP, "backends", backends)
		if err := r.LoadBalancer.UpdateBackends(ctx, instance.IP, keys, backends); err != nil {
			// The instance is running but sshd/haproxy may not be up yet
			// right after boot; keep trying.
			setLBNotReady(verdaCluster, "BackendUpdateFailed", err.Error())
			log.Info("Load balancer backend update failed, will retry", "err", err.Error())
			// Publishing the endpoint is what lets the control plane start,
			// so the cluster is provisioned even while the first sync retries.
			verdaCluster.Status.Initialization.Provisioned = ptr.To(true)
			return ctrl.Result{RequeueAfter: lbPollInterval}, nil
		}
		verdaCluster.Status.LoadBalancer.Backends = backends
	}

	conditions.Set(verdaCluster, metav1.Condition{
		Type:   infrav1.LoadBalancerReadyCondition,
		Status: metav1.ConditionTrue,
		Reason: clusterv1.ReadyReason,
	})
	verdaCluster.Status.Initialization.Provisioned = ptr.To(true)
	return ctrl.Result{RequeueAfter: lbResyncInterval}, nil
}

// controlPlaneAddresses returns the sorted external IPs of the cluster's
// control plane VerdaMachines that are not being deleted.
func (r *VerdaClusterReconciler) controlPlaneAddresses(ctx context.Context, cluster *clusterv1.Cluster) ([]string, error) {
	verdaMachines := &infrav1.VerdaMachineList{}
	if err := r.List(ctx, verdaMachines, client.InNamespace(cluster.Namespace), client.MatchingLabels{clusterv1.ClusterNameLabel: cluster.Name}); err != nil {
		return nil, fmt.Errorf("listing VerdaMachines: %w", err)
	}
	var addresses []string
	for i := range verdaMachines.Items {
		vm := &verdaMachines.Items[i]
		if !vm.DeletionTimestamp.IsZero() {
			continue
		}
		isControlPlane, err := r.isControlPlane(ctx, vm)
		if err != nil {
			return nil, err
		}
		if !isControlPlane {
			continue
		}
		for _, addr := range vm.Status.Addresses {
			if addr.Type == clusterv1.MachineExternalIP && addr.Address != "" {
				addresses = append(addresses, addr.Address)
				break
			}
		}
	}
	slices.Sort(addresses)
	return addresses, nil
}

// isControlPlane reports whether a VerdaMachine backs a control plane Machine,
// checking its own labels first and then the owning Machine's.
func (r *VerdaClusterReconciler) isControlPlane(ctx context.Context, vm *infrav1.VerdaMachine) (bool, error) {
	if _, ok := vm.Labels[clusterv1.MachineControlPlaneLabel]; ok {
		return true, nil
	}
	machine, err := util.GetOwnerMachine(ctx, r.Client, vm.ObjectMeta)
	if err != nil {
		return false, err
	}
	return machine != nil && util.IsControlPlaneMachine(machine), nil
}

func (r *clusterScope) findLoadBalancer(ctx context.Context, verdaCluster *infrav1.VerdaCluster) (*cloud.Instance, error) {
	if id := verdaCluster.Status.LoadBalancer.InstanceID; id != "" {
		instance, err := r.cloud.GetInstance(ctx, id)
		if err == nil {
			return instance, nil
		}
		if !errors.Is(err, cloud.ErrNotFound) {
			return nil, err
		}
		return &cloud.Instance{ID: id, Status: cloud.StatusNotFound}, nil
	}
	instance, err := r.cloud.FindInstanceByTag(ctx, cloud.TagLoadBalancer, clusterTagValue(verdaCluster))
	if err != nil {
		if errors.Is(err, cloud.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return instance, nil
}

func (r *clusterScope) createLoadBalancer(ctx context.Context, cluster *clusterv1.Cluster, verdaCluster *infrav1.VerdaCluster, keys *loadbalancer.Keys) (*cloud.Instance, error) {
	lb := verdaCluster.Spec.ControlPlaneLoadBalancer
	instanceType := lb.InstanceType
	if instanceType == "" {
		instanceType = infrav1.DefaultLoadBalancerInstanceType
	}
	image := lb.Image
	if image == "" {
		image = infrav1.DefaultLoadBalancerImage
	}
	return r.cloud.CreateInstance(ctx, cloud.InstanceSpec{
		Hostname:      loadBalancerHostname(verdaCluster),
		Description:   fmt.Sprintf("Cluster API control plane load balancer for cluster %s/%s", cluster.Namespace, cluster.Name),
		InstanceType:  instanceType,
		Image:         image,
		Location:      verdaCluster.Spec.Location,
		SSHKeyIDs:     lb.SSHKeyIDs,
		StartupScript: loadbalancer.StartupScript(keys, nil),
		Tags: map[string]string{
			cloud.TagManagedBy:    cloud.ManagedByValue,
			cloud.TagCluster:      cluster.Namespace + "/" + cluster.Name,
			cloud.TagLoadBalancer: clusterTagValue(verdaCluster),
			"capi-role":           "load-balancer",
		},
	})
}

// ensureLoadBalancerKeys returns the SSH keys for the load balancer, creating
// the Secret that holds them on first use.
func (r *VerdaClusterReconciler) ensureLoadBalancerKeys(ctx context.Context, cluster *clusterv1.Cluster, verdaCluster *infrav1.VerdaCluster) (*loadbalancer.Keys, error) {
	secret := &corev1.Secret{}
	key := client.ObjectKey{Namespace: verdaCluster.Namespace, Name: loadBalancerSecretName(verdaCluster)}
	err := r.Get(ctx, key, secret)
	if err == nil {
		return keysFromSecret(secret)
	}
	if !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("getting load balancer secret %s: %w", key, err)
	}

	keys, err := loadbalancer.GenerateKeys()
	if err != nil {
		return nil, err
	}
	secret = &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      key.Name,
			Namespace: key.Namespace,
			Labels:    map[string]string{clusterv1.ClusterNameLabel: cluster.Name},
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(verdaCluster,
				infrav1.GroupVersion.WithKind(verdaClusterKind))},
		},
		Type: corev1.SecretTypeSSHAuth,
		Data: map[string][]byte{
			secretKeyClientPrivate: keys.ClientPrivateKey,
			secretKeyClientPublic:  keys.ClientPublicKey,
			secretKeyHostPrivate:   keys.HostPrivateKey,
			secretKeyHostPublic:    keys.HostPublicKey,
		},
	}
	if err := r.Create(ctx, secret); err != nil {
		if apierrors.IsAlreadyExists(err) {
			// Lost a race with a concurrent reconcile; read what won.
			if err := r.Get(ctx, key, secret); err != nil {
				return nil, err
			}
			return keysFromSecret(secret)
		}
		return nil, fmt.Errorf("creating load balancer secret %s: %w", key, err)
	}
	ctrl.LoggerFrom(ctx).Info("Created load balancer SSH keys", "Secret", key)
	return keys, nil
}

func keysFromSecret(secret *corev1.Secret) (*loadbalancer.Keys, error) {
	keys := &loadbalancer.Keys{
		ClientPrivateKey: secret.Data[secretKeyClientPrivate],
		ClientPublicKey:  secret.Data[secretKeyClientPublic],
		HostPrivateKey:   secret.Data[secretKeyHostPrivate],
		HostPublicKey:    secret.Data[secretKeyHostPublic],
	}
	if len(keys.ClientPrivateKey) == 0 || len(keys.ClientPublicKey) == 0 || len(keys.HostPrivateKey) == 0 || len(keys.HostPublicKey) == 0 {
		return nil, fmt.Errorf("load balancer secret %s/%s is missing keys", secret.Namespace, secret.Name)
	}
	return keys, nil
}

func (r *clusterScope) reconcileDelete(ctx context.Context, verdaCluster *infrav1.VerdaCluster) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)

	// Machines clean up their own instances; Cluster API waits for all
	// Machines to be gone before deleting the InfraCluster. Only the load
	// balancer is ours to remove.
	instance, err := r.findLoadBalancer(ctx, verdaCluster)
	if err != nil {
		return ctrl.Result{}, err
	}
	if instance != nil && !instanceGone(instance) {
		setLBNotReady(verdaCluster, clusterv1.DeletingReason, "Deleting load balancer instance")
		if instance.Status != cloud.StatusDeleting {
			log.Info("Deleting load balancer instance", "instanceID", instance.ID)
			if err := r.cloud.DeleteInstance(ctx, instance.ID); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{RequeueAfter: deletePollInterval}, nil
	}
	if id := verdaCluster.Status.LoadBalancer.StartupScriptID; id != "" {
		if err := r.cloud.DeleteStartupScript(ctx, id); err != nil {
			return ctrl.Result{}, err
		}
		verdaCluster.Status.LoadBalancer.StartupScriptID = ""
	} else if verdaCluster.LoadBalancerEnabled() {
		if err := r.cloud.DeleteStartupScriptByName(ctx, loadBalancerHostname(verdaCluster)); err != nil {
			return ctrl.Result{}, err
		}
	}
	// The key Secret is garbage collected through its owner reference.

	controllerutil.RemoveFinalizer(verdaCluster, infrav1.ClusterFinalizer)
	return ctrl.Result{}, nil
}

func setEndpointReady(verdaCluster *infrav1.VerdaCluster) {
	conditions.Set(verdaCluster, metav1.Condition{
		Type:   infrav1.ControlPlaneEndpointReadyCondition,
		Status: metav1.ConditionTrue,
		Reason: clusterv1.ReadyReason,
	})
}

func setLBNotReady(verdaCluster *infrav1.VerdaCluster, reason, message string) {
	conditions.Set(verdaCluster, metav1.Condition{
		Type:    infrav1.LoadBalancerReadyCondition,
		Status:  metav1.ConditionFalse,
		Reason:  reason,
		Message: message,
	})
	if reason == "InstanceProvisioning" || reason == "InstanceCreateFailed" || reason == "InstanceFailed" {
		conditions.Set(verdaCluster, metav1.Condition{
			Type:    infrav1.ControlPlaneEndpointReadyCondition,
			Status:  metav1.ConditionFalse,
			Reason:  "WaitingForLoadBalancer",
			Message: message,
		})
	}
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
	case verdaCluster.LoadBalancerEnabled() && !conditions.IsTrue(verdaCluster, infrav1.LoadBalancerReadyCondition):
		ready.Status = metav1.ConditionFalse
		ready.Reason = clusterv1.NotReadyReason
		ready.Message = conditions.GetMessage(verdaCluster, infrav1.LoadBalancerReadyCondition)
	}
	conditions.Set(verdaCluster, ready)

	return patchHelper.Patch(ctx, verdaCluster, patch.WithOwnedConditions{Conditions: []string{
		clusterv1.PausedCondition,
		clusterv1.ReadyCondition,
		infrav1.ControlPlaneEndpointReadyCondition,
		infrav1.LoadBalancerReadyCondition,
	}})
}

// clusterTagValue identifies a VerdaCluster's load balancer instance.
func clusterTagValue(verdaCluster *infrav1.VerdaCluster) string {
	return truncateTag(verdaCluster.Namespace + "/" + verdaCluster.Name)
}

func loadBalancerHostname(verdaCluster *infrav1.VerdaCluster) string {
	return verdaCluster.Name + "-lb"
}

func loadBalancerSecretName(verdaCluster *infrav1.VerdaCluster) string {
	return verdaCluster.Name + "-lb-ssh"
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
			handler.EnqueueRequestsFromMapFunc(util.ClusterToInfrastructureMapFunc(ctx, infrav1.GroupVersion.WithKind(verdaClusterKind), mgr.GetClient(), &infrav1.VerdaCluster{})),
			builder.WithPredicates(predicates.ClusterPausedTransitions(mgr.GetScheme(), predicateLog)),
		).
		// Control plane machines coming and going change the backend list.
		Watches(
			&infrav1.VerdaMachine{},
			handler.EnqueueRequestsFromMapFunc(r.verdaMachineToVerdaCluster),
		).
		Named("verdacluster").
		Complete(r)
}

// verdaMachineToVerdaCluster maps a VerdaMachine to the VerdaCluster of the
// Cluster named in its cluster-name label.
func (r *VerdaClusterReconciler) verdaMachineToVerdaCluster(ctx context.Context, o client.Object) []reconcile.Request {
	clusterName, ok := o.GetLabels()[clusterv1.ClusterNameLabel]
	if !ok {
		return nil
	}
	cluster := &clusterv1.Cluster{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: o.GetNamespace(), Name: clusterName}, cluster); err != nil {
		return nil
	}
	if !cluster.Spec.InfrastructureRef.IsDefined() || cluster.Spec.InfrastructureRef.Kind != verdaClusterKind {
		return nil
	}
	return []reconcile.Request{{NamespacedName: client.ObjectKey{Namespace: cluster.Namespace, Name: cluster.Spec.InfrastructureRef.Name}}}
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
